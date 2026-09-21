//go:build integration

package ai_assessments

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const a2TablesSHA = "49e13f82d9fac5f35b3783b47e69403074929059049863f9a3bd1f2e90328cdf"
const a2ClientPreface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

type a2Header struct{ Name, Value string }
type a2HpackTables struct {
	HuffmanCodes   []uint32
	HuffmanLengths []uint8
	StaticTable    []a2Header
}
type a2Hpack struct {
	tables  a2HpackTables
	dynamic []a2Header
	size    int
	maxSize int
	huffman map[uint64]byte
}

func a2ReadTables(t *testing.T) a2HpackTables {
	t.Helper()
	root := os.Getenv("ASPM_ASSESSMENT_A2_REVIEW")
	if root == "" {
		root = filepath.Join("reviews", "v1")
	}
	data, err := os.ReadFile(filepath.Join(root, "HPACK-TABLES-A2.json"))
	must(t, "read pinned protocol tables", err)
	sum := sha256.Sum256(data)
	check(t, hex.EncodeToString(sum[:]) == a2TablesSHA, "pinned Go protocol table bytes changed")
	var tables a2HpackTables
	must(t, "decode bounded protocol table data", json.Unmarshal(data, &tables))
	check(t, len(tables.HuffmanCodes) == 256 && len(tables.HuffmanLengths) == 256 && len(tables.StaticTable) == 61, "invalid protocol table dimensions")
	return tables
}
func a2NewHpack(tables a2HpackTables) *a2Hpack {
	h := &a2Hpack{tables: tables, maxSize: 4096, huffman: map[uint64]byte{}}
	for i, code := range tables.HuffmanCodes {
		h.huffman[uint64(tables.HuffmanLengths[i])<<32|uint64(code)] = byte(i)
	}
	return h
}
func a2Integer(data *[]byte, prefix uint8) (int, error) {
	if len(*data) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	mask := byte((1 << prefix) - 1)
	n := int((*data)[0] & mask)
	*data = (*data)[1:]
	if n != int(mask) {
		return n, nil
	}
	for shift := uint(0); shift <= 28; shift += 7 {
		if len(*data) == 0 {
			return 0, io.ErrUnexpectedEOF
		}
		b := (*data)[0]
		*data = (*data)[1:]
		n += int(b&127) << shift
		if n > 256<<10 {
			return 0, errors.New("owned HPACK integer exceeds bound")
		}
		if b&128 == 0 {
			return n, nil
		}
	}
	return 0, errors.New("owned HPACK integer overflow")
}
func (h *a2Hpack) text(data *[]byte) (string, error) {
	if len(*data) == 0 {
		return "", io.ErrUnexpectedEOF
	}
	compressed := (*data)[0]&128 != 0
	n, err := a2Integer(data, 7)
	if err != nil || n > len(*data) {
		return "", io.ErrUnexpectedEOF
	}
	raw := (*data)[:n]
	*data = (*data)[n:]
	if !compressed {
		return string(raw), nil
	}
	var out []byte
	var code uint32
	var bits uint8
	for _, b := range raw {
		for shift := 7; shift >= 0; shift-- {
			code = code<<1 | uint32((b>>shift)&1)
			bits++
			if symbol, ok := h.huffman[uint64(bits)<<32|uint64(code)]; ok {
				out = append(out, symbol)
				code, bits = 0, 0
			} else if bits > 30 {
				return "", errors.New("invalid owned HPACK Huffman data")
			}
			if len(out) > 32768 {
				return "", errors.New("owned header text bound exceeded")
			}
		}
	}
	if bits > 7 || code != (uint32(1)<<bits)-1 {
		return "", errors.New("invalid owned Huffman padding")
	}
	return string(out), nil
}
func (h *a2Hpack) at(index int) (a2Header, error) {
	if index < 1 {
		return a2Header{}, errors.New("invalid header index")
	}
	if index <= len(h.tables.StaticTable) {
		return h.tables.StaticTable[index-1], nil
	}
	index -= len(h.tables.StaticTable) + 1
	if index >= len(h.dynamic) {
		return a2Header{}, errors.New("invalid dynamic header index")
	}
	return h.dynamic[index], nil
}
func (h *a2Hpack) evict() {
	for h.size > h.maxSize && len(h.dynamic) > 0 {
		last := h.dynamic[len(h.dynamic)-1]
		h.size -= 32 + len(last.Name) + len(last.Value)
		h.dynamic = h.dynamic[:len(h.dynamic)-1]
	}
}
func (h *a2Hpack) decode(data []byte) (map[string]string, error) {
	result := map[string]string{}
	for count := 0; len(data) != 0; count++ {
		if count > 128 {
			return nil, errors.New("owned header count exceeded")
		}
		first := data[0]
		if first&128 != 0 {
			index, err := a2Integer(&data, 7)
			if err != nil {
				return nil, err
			}
			field, err := h.at(index)
			if err != nil {
				return nil, err
			}
			result[field.Name] = field.Value
			continue
		}
		if first&0xe0 == 0x20 {
			value, err := a2Integer(&data, 5)
			if err != nil || value > 4096 {
				return nil, errors.New("invalid owned dynamic table bound")
			}
			h.maxSize = value
			h.evict()
			continue
		}
		increment := first&0x40 != 0
		prefix := uint8(4)
		if increment {
			prefix = 6
		}
		index, err := a2Integer(&data, prefix)
		if err != nil {
			return nil, err
		}
		var field a2Header
		if index == 0 {
			field.Name, err = h.text(&data)
		} else {
			field, err = h.at(index)
		}
		if err != nil {
			return nil, err
		}
		field.Value, err = h.text(&data)
		if err != nil {
			return nil, err
		}
		result[field.Name] = field.Value
		if increment {
			h.dynamic = append([]a2Header{field}, h.dynamic...)
			h.size += 32 + len(field.Name) + len(field.Value)
			h.evict()
		}
	}
	return result, nil
}

type a2Frame struct {
	kind, flags byte
	stream      uint32
	payload     []byte
}

func a2ReadFrame(r io.Reader) (a2Frame, error) {
	var header [9]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return a2Frame{}, err
	}
	n := int(header[0])<<16 | int(header[1])<<8 | int(header[2])
	if n > 128<<10 {
		return a2Frame{}, errors.New("owned frame bound exceeded")
	}
	f := a2Frame{kind: header[3], flags: header[4], stream: binary.BigEndian.Uint32(header[5:]) & 0x7fffffff, payload: make([]byte, n)}
	_, err := io.ReadFull(r, f.payload)
	return f, err
}
func a2WriteFrame(w io.Writer, kind, flags byte, stream uint32, payload []byte) error {
	header := []byte{byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload)), kind, flags, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(header[5:], stream)
	data := append(header, payload...)
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}
func a2String(value string) []byte {
	n := len(value)
	result := []byte{}
	if n < 127 {
		result = append(result, byte(n))
	} else {
		result = append(result, 127)
		n -= 127
		for n >= 128 {
			result = append(result, byte(n&127)|128)
			n >>= 7
		}
		result = append(result, byte(n))
	}
	return append(result, value...)
}
func a2Literal(name, value string) []byte {
	result := []byte{0}
	result = append(result, a2String(name)...)
	return append(result, a2String(value)...)
}

type a2NativeStream struct {
	Connection int
	Stream     uint32
	Protocol   string
	Method     string
	Path       string
	BodyBytes  int
	BodyDigest string
}
type a2RefusedServer struct {
	t        *testing.T
	listener net.Listener
	address  string
	client   *http.Client
	tables   a2HpackTables
	mu       sync.Mutex
	conns    []net.Conn
	records  []a2NativeStream
	headers  []a2NativeStream
	refused  int
	verified int
	wantKey  string
	wantBody []byte
	job      *assessment
	h        *harness
	wg       sync.WaitGroup
}

func a2Certificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, "generate owned ephemeral TLS key in memory", err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(20260921), Subject: pkix.Name{CommonName: "owned-a2-native"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	raw, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	must(t, "create only owned loopback certificate", err)
	parsed, err := x509.ParseCertificate(raw)
	must(t, "parse owned public certificate", err)
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{raw}, PrivateKey: key}, pool
}
func newA2RefusedServer(t *testing.T) *a2RefusedServer {
	t.Helper()
	certificate, roots := a2Certificate(t)
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "listen only on owned TLS endpoint", err)
	s := &a2RefusedServer{t: t, address: raw.Addr().String(), tables: a2ReadTables(t)}
	s.listener = tls.NewListener(raw, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}})
	tr := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 2 * time.Second,
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || state.NegotiatedProtocol != "h2" {
				t.Error("A2 requires normal verified TLS and negotiated HTTP2, not an H1 fallback")
				return errors.New("owned verified HTTP2 boundary not reached")
			}
			s.mu.Lock()
			s.verified++
			s.mu.Unlock()
			return nil
		}},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != s.address {
				t.Error("A2 HTTP2 dial left the owned endpoint")
				return nil, errors.New("unowned A2 native address")
			}
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
		}}
	s.client = &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := s.listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			if len(s.conns) >= 3 {
				s.mu.Unlock()
				_ = conn.Close()
				t.Error("A2 owned connection ceiling exceeded")
				return
			}
			s.conns = append(s.conns, conn)
			index := len(s.conns)
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(6 * time.Second))
				tlsConn := conn.(*tls.Conn)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				err := tlsConn.HandshakeContext(ctx)
				cancel()
				if err != nil {
					return
				}
				if tlsConn.ConnectionState().NegotiatedProtocol == "h2" {
					s.serveH2(conn, index)
				} else {
					t.Error("A2 worker did not negotiate the required HTTP2 capability")
				}
			}()
		}
	}()
	t.Cleanup(func() {
		tr.CloseIdleConnections()
		_ = s.listener.Close()
		s.mu.Lock()
		conns := append([]net.Conn{}, s.conns...)
		s.mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		s.wg.Wait()
	})
	return s
}
func (s *a2RefusedServer) url() string { return "https://" + s.address }
func (s *a2RefusedServer) observed() ([]a2NativeStream, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]a2NativeStream{}, s.records...), s.refused
}
func (s *a2RefusedServer) headerObservations() ([]a2NativeStream, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]a2NativeStream{}, s.headers...), s.verified
}
func (s *a2RefusedServer) recordHeaders(conn int, stream uint32, headers map[string]string) bool {
	if headers[":method"] != "POST" || headers[":path"] != "/v1/responses" || headers[":authority"] != s.address ||
		headers[":scheme"] != "https" || headers["authorization"] != "Bearer "+s.wantKey ||
		headers["content-type"] != "application/json" || stream == 0 || stream%2 != 1 {
		s.t.Error("A2 actual HTTP2 POST/header/authority mapping differs")
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.headers) >= 3 {
		s.t.Error("A2 decoded POST HEADERS ceiling exceeded")
		return false
	}
	for _, old := range s.headers {
		if old.Connection == conn && old.Stream == stream {
			s.t.Error("A2 repeated HEADERS on an already observed stream")
			return false
		}
	}
	s.headers = append(s.headers, a2NativeStream{Connection: conn, Stream: stream, Protocol: "h2", Method: "POST", Path: "/v1/responses"})
	return true
}
func (s *a2RefusedServer) record(protocol string, conn int, stream uint32, headers map[string]string, body []byte) int {
	if headers[":method"] != "POST" || headers[":path"] != "/v1/responses" || headers[":authority"] != s.address ||
		headers["authorization"] != "Bearer "+s.wantKey || !strings.HasPrefix(headers["content-type"], "application/json") {
		s.t.Error("A2 actual native POST/header/authority mapping differs")
	}
	if s.wantBody != nil && !bytes.Equal(body, s.wantBody) {
		s.t.Error("A2 standard transport did not preserve replayed body bytes")
	}
	if s.job != nil {
		var input map[string]any
		if json.Unmarshal(body, &input) != nil {
			s.t.Error("A2 native JSON invalid")
		}
		s.t.Helper()
		if input["model"] != s.job.Model || input["stream"] != false || input["store"] != false || input["max_output_tokens"] != float64(128) || input["tools"] != nil || input["tool_choice"] != nil {
			s.t.Error("A2 native Responses model/privacy/token/tool mapping differs")
		}
		allowed := map[string]bool{"model": true, "stream": true, "store": true, "max_output_tokens": true, "input": true, "text": true}
		for field := range input {
			if !allowed[field] {
				s.t.Error("A2 native Responses included an unapproved field")
			}
		}
		messages, ok := input["input"].([]any)
		want := "Evidence identifier: " + s.job.ContextRef + "\nEvidence SHA-256: " + s.job.ContextDigest +
			"\nBEGIN UNTRUSTED EVIDENCE\n" + s.job.Context + "\nEND UNTRUSTED EVIDENCE"
		if !ok || len(messages) != 2 || objectAt(messages[0], "role") != "system" || objectAt(messages[1], "role") != "user" ||
			objectAt(messages[0], "content") != serverInstructions || objectAt(messages[1], "content") != want {
			s.t.Error("A2 exact trusted prompt/approved context changed")
		}
		var schema any
		_ = json.Unmarshal([]byte(assessmentSchema), &schema)
		if !reflect.DeepEqual(objectAt(input, "text", "format", "schema"), schema) || objectAt(input, "text", "format", "strict") != true ||
			objectAt(input, "text", "format", "type") != "json_schema" {
			s.t.Error("A2 structured-output schema changed")
		}
		s.h.noSecrets(body)
		if bytes.Contains(body, []byte("SYNTHETIC-RAW-NOT-APPROVED")) || bytes.Contains(body, []byte("SYNTHETIC-NOTE-NOT-APPROVED")) {
			s.t.Error("A2 unapproved intake or notes crossed the native boundary")
		}
		probe, cancel := context.WithTimeout(s.h.ctx, time.Second)
		var state string
		var attempts int
		var marker *time.Time
		err := s.h.db.QueryRow(probe, "SELECT state,attempts,dispatch_started_at FROM "+s.h.table("assessment_jobs")+" WHERE id=$1", s.job.ID).Scan(&state, &attempts, &marker)
		cancel()
		if err != nil || state != "dispatching" || attempts != 1 || marker == nil {
			s.t.Error("A2 complete native POST lacks a committed single-attempt marker")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, a2NativeStream{conn, stream, protocol, headers[":method"], headers[":path"], len(body), digest(body)})
	if len(s.records) > 3 {
		s.t.Error("A2 owned native request ceiling exceeded")
	}
	return len(s.records)
}
func (s *a2RefusedServer) response() []byte {
	if s.job == nil {
		return []byte(`{"owned":"transport calibration only"}`)
	}
	text := string(encoded(s.t, map[string]any{"conclusion": "inconclusive", "uncertainty": "Synthetic response after a refused stream; not remote processing proof.", "evidenceRefs": []string{s.job.ContextRef}}))
	return encoded(s.t, map[string]any{"model": "owned-h2-returned-model", "status": "completed", "output": []any{
		map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": text}}}},
		"usage": map[string]int{"input_tokens": 11, "output_tokens": 7}})
}
func (s *a2RefusedServer) serveH2(conn net.Conn, index int) {
	preface := make([]byte, len(a2ClientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil || string(preface) != a2ClientPreface {
		s.t.Error("A2 negotiated h2 without actual client preface")
		return
	}
	if err := a2WriteFrame(conn, 4, 0, 0, nil); err != nil {
		s.t.Error("A2 failed to send owned HTTP2 SETTINGS")
		return
	}
	decoder := a2NewHpack(s.tables)
	headers := map[uint32]map[string]string{}
	bodies := map[uint32][]byte{}
	blocks := map[uint32][]byte{}
	headerEnd := map[uint32]bool{}
	var continuation uint32
	finish := func(stream uint32) {
		if headers[stream] == nil {
			s.t.Error("A2 complete body without decoded native POST HEADERS")
			return
		}
		count := s.record("h2", index, stream, headers[stream], bodies[stream])
		delete(headers, stream)
		delete(bodies, stream)
		if count == 1 {
			if err := a2WriteFrame(conn, 3, 0, stream, []byte{0, 0, 0, 7}); err != nil {
				s.t.Error("A2 did not send the actual REFUSED_STREAM")
				return
			}
			s.mu.Lock()
			s.refused++
			s.mu.Unlock()
			return
		}
		response := s.response()
		block := []byte{0x88}
		block = append(block, a2Literal("content-type", "application/json")...)
		block = append(block, a2Literal("x-request-id", "owned-h2-request")...)
		block = append(block, a2Literal("content-length", strconv.Itoa(len(response)))...)
		if a2WriteFrame(conn, 1, 4, stream, block) != nil || a2WriteFrame(conn, 0, 1, stream, response) != nil {
			s.t.Error("A2 did not send the bounded native response")
		}
	}
	totalBytes := 0
	for count := 0; count < 128; count++ {
		frame, err := a2ReadFrame(conn)
		if err != nil {
			return
		}
		totalBytes += 9 + len(frame.payload)
		if totalBytes > 512<<10 || (continuation != 0 && (frame.kind != 9 || frame.stream != continuation)) {
			s.t.Error("A2 framed input bound or HEADERS/CONTINUATION ordering violated")
			return
		}
		switch frame.kind {
		case 4:
			if frame.stream != 0 || len(frame.payload)%6 != 0 || (frame.flags&1 != 0 && len(frame.payload) != 0) {
				s.t.Error("A2 invalid SETTINGS")
				return
			}
			if frame.flags&1 == 0 {
				_ = a2WriteFrame(conn, 4, 1, 0, nil)
			}
		case 6:
			if frame.stream != 0 || len(frame.payload) != 8 {
				s.t.Error("A2 invalid PING")
				return
			}
			if frame.flags&1 == 0 {
				_ = a2WriteFrame(conn, 6, 1, 0, frame.payload)
			}
		case 1, 9:
			if frame.kind == 9 && continuation != frame.stream {
				s.t.Error("A2 unexpected CONTINUATION")
				return
			}
			data := frame.payload
			if frame.kind == 1 {
				headerEnd[frame.stream] = frame.flags&1 != 0
				if frame.flags&8 != 0 {
					if len(data) == 0 || int(data[0])+1 > len(data) {
						return
					}
					data = data[1 : len(data)-int(data[0])]
				}
				if frame.flags&32 != 0 {
					if len(data) < 5 {
						return
					}
					data = data[5:]
				}
			}
			blocks[frame.stream] = append(blocks[frame.stream], data...)
			if len(blocks[frame.stream]) > 64<<10 {
				s.t.Error("A2 HPACK block bound")
				return
			}
			if frame.flags&4 != 0 {
				decoded, err := decoder.decode(blocks[frame.stream])
				if err != nil {
					s.t.Errorf("A2 bounded HPACK decode failed (%T)", err)
					return
				}
				headers[frame.stream] = decoded
				delete(blocks, frame.stream)
				continuation = 0
				if !s.recordHeaders(index, frame.stream, decoded) {
					return
				}
				if headerEnd[frame.stream] {
					finish(frame.stream)
				}
				delete(headerEnd, frame.stream)
			} else {
				continuation = frame.stream
			}
		case 0:
			data := frame.payload
			if frame.flags&8 != 0 {
				if len(data) == 0 || int(data[0])+1 > len(data) {
					return
				}
				data = data[1 : len(data)-int(data[0])]
			}
			bodies[frame.stream] = append(bodies[frame.stream], data...)
			if len(bodies[frame.stream]) > 128<<10 {
				s.t.Error("A2 native body bound")
				return
			}
			if frame.flags&1 != 0 {
				finish(frame.stream)
			}
		case 7:
			return
		}
	}
	s.t.Error("A2 received frame ceiling exceeded")
}

func calibrateA2HTTP2(t *testing.T) {
	for _, rewindable := range []bool{true, false} {
		name := "rewindable-two-streams"
		if !rewindable {
			name = "nonrewindable-one-stream-fixture-control"
		}
		t.Run(name, func(t *testing.T) {
			server := newA2RefusedServer(t)
			server.wantKey = "synthetic-standard-transport-only"
			server.wantBody = []byte(`{"calibration":"owned POST, no model account"}`)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, "POST", server.url()+"/v1/responses", bytes.NewReader(server.wantBody))
			must(t, "create standard replayable POST", err)
			check(t, request.GetBody != nil, "standard bytes.NewReader request was not rewindable")
			if !rewindable {
				request.GetBody = nil
			}
			request.Header.Set("Authorization", "Bearer "+server.wantKey)
			request.Header.Set("Content-Type", "application/json")
			response, err := server.client.Do(request)
			wantStreams := 1
			if rewindable {
				wantStreams = 2
				must(t, "unmodified pinned standard transport after real REFUSED_STREAM", err)
				check(t, response.ProtoMajor == 2 && response.TLS != nil && len(response.TLS.VerifiedChains) > 0, "calibration did not use verified HTTP2")
				data, readErr := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
				must(t, "read real bounded calibration response", readErr)
				must(t, "close real calibration response", response.Body.Close())
				check(t, len(data) <= 128<<10, "calibration response exceeded its bound")
			} else {
				if response != nil {
					_ = response.Body.Close()
				}
				check(t, err != nil, "nonrewindable fixture control unexpectedly received a response")
			}
			records, refused := server.observed()
			headers, verified := server.headerObservations()
			check(t, len(headers) == wantStreams && len(records) == wantStreams && refused == 1 && verified == 1,
				"actual standard transport did not demonstrate the calibrated HTTP2 stream count")
			for _, r := range records {
				check(t, r.Protocol == "h2" && r.Method == "POST" && r.BodyBytes == len(server.wantBody) && r.BodyDigest == digest(server.wantBody),
					"calibration stream lacked the complete exact native POST body")
			}
			if rewindable {
				check(t, records[0].Connection == records[1].Connection && records[0].Stream != records[1].Stream &&
					records[0].BodyDigest == records[1].BodyDigest, "calibration did not reuse the connection for distinct identical-body POST streams")
				t.Logf("A2 HTTP2 replay calibration: sameConnection=true distinctStreams=true identicalBody=true streams=%d,%d",
					records[0].Stream, records[1].Stream)
			}
			t.Logf("A2 HTTP2 calibration: negotiated=h2 verifiedTLS=%d rewindable=%t actualPOSTHeaders=%d completePOSTBodies=%d refused=%d; fixture proof only, no processing/billing exactly-once claim",
				verified, rewindable, len(headers), len(records), refused)
		})
	}
}

func TestAA9NativeHTTP2RefusedStreamCannotReplayAssessmentPOST(t *testing.T) {
	h := newHarness(t, false)
	finding := h.seed()
	server := newA2RefusedServer(t)
	key := secret(t)
	h.remember(key)
	p := h.json(h.admin, "POST", profilesPath, map[string]any{"name": "Owned HTTP2 assessment regression", "family": "openai",
		"endpoint": server.url(), "model": "operator-selected-model", "deployment": "", "enabled": true, "structuredOutput": true, "apiKey": key}, 201).Profile
	pol, grant := h.approve(p)
	v := h.preview(h.admin, finding, p, pol, grant, reviewedContext())
	job := h.enqueue(h.admin, v, "one-native-attempt", 202)
	server.wantKey, server.job, server.h = key, &job, h
	before, storage := h.domainSnapshot(), h.storageCalls.Load()
	config := h.configForWorker("http2-single-attempt")
	config.Client = server.client
	worker := h.worker(config)
	process(t, h.ctx, worker, true)
	actual := h.job(h.admin, job.ID)
	records, refused := server.observed()
	headers, verified := server.headerObservations()
	var charges int
	must(t, "readonly actual durable attempt-window observation", h.db.QueryRow(h.ctx,
		"SELECT count(*) FROM "+h.table("assessment_jobs")+" WHERE scope=$1 AND dispatch_started_at>clock_timestamp()-interval '1 minute'", h.scope).Scan(&charges))
	t.Logf("A2 worker native observation: protocol=%s verifiedTLS=%d actualPOSTHeaders=%d completePOSTBodies=%d refused=%d DBattempts=%d durableWindowCharges=%d state=%s requestsPerWindow=%d; actual request streams, not outer Do calls",
		func() string {
			if len(records) > 0 {
				return records[0].Protocol
			}
			return "none"
		}(), verified, len(headers), len(records), refused, actual.Attempts, charges, actual.State, config.RequestsPerWindow)
	check(t, len(headers) > 0 && len(records) > 0 && records[0].Protocol == "h2" && refused == 1 && verified > 0,
		"BLOCKED: required verified HTTP2/native refused stream not exercised; H1-only or zero-request bypass is forbidden")
	check(t, actual.Attempts == 1 && actual.DispatchStartedAt != nil && charges == 1, "actual durable single attempt/window charge was not established")
	if len(headers) != 1 || len(records) != 1 {
		t.Error("ONE recorded attempt/quota charge emitted more than ONE actual native POST stream through internal transport replay")
	}
	if actual.Result != nil || actual.State == "succeeded" {
		t.Error("refused single native attempt committed a successful internal retry output")
	}
	initialHeaders, initialBodies := len(headers), len(records)
	process(t, h.ctx, worker, false)
	records, _ = server.observed()
	headers, _ = server.headerObservations()
	check(t, len(headers) == initialHeaders && len(records) == initialBodies, "terminal attempt was silently resubmitted by a later scheduling step")
	h.assertReadonly(before, storage)
	t.Log("A2 HTTP2 executed observation tail: no later scheduler resend; finding/domain/storage unchanged. This does not erase earlier replay/result failures.")
}
