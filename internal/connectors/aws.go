package connectors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	EC2APIVersion = "2016-11-15"
	ASFFVersion   = "2018-10-08"
)

var (
	awsAccount = regexp.MustCompile(`^[0-9]{12}$`)
	awsRegion  = regexp.MustCompile(`^(af|ap|ca|eu|il|me|mx|sa|us)-(central|north|south|east|west|northeast|northwest|southeast|southwest)-[1-9][0-9]*$`)
	instanceID = regexp.MustCompile(`^i-([0-9a-f]{8}|[0-9a-f]{17})$`)
)

type awsCollector struct {
	collectorBase
	findings *url.URL
}

func awsEndpointScope(endpoint *url.URL, service, region string) bool {
	host := strings.ToLower(endpoint.Hostname())
	if strings.HasSuffix(host, ".amazonaws.com") || strings.HasSuffix(host, ".api.aws") {
		return host == service+"."+region+".amazonaws.com" || host == service+"."+region+".api.aws"
	}
	// An explicitly injected TLS gateway/client is also supported. It is never
	// discovered from environment variables, AWS profiles, metadata, or STS.
	return true
}

func (c *awsCollector) Collect(ctx context.Context, request CollectRequest) (Collection, error) {
	result, err := startCollection(ctx, request.Identity)
	if err != nil {
		return result, err
	}
	if !awsAccount.MatchString(request.AccountID) || !awsRegion.MatchString(request.Region) ||
		!awsEndpointScope(c.base, "ec2", request.Region) || !awsEndpointScope(c.findings, "securityhub", request.Region) {
		return result.fail("scope", connectorError(ErrScope))
	}
	credentials, err := c.config.Credentials.Retrieve(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return result.fail("credentials", connectorError(ctx.Err()))
		}
		return result.fail("credentials", connectorError(ErrAuth))
	}
	if !textID(credentials.AccessKeyID, 256) || !textID(credentials.SecretAccessKey, 1024) ||
		credentials.SessionToken != "" && !textID(credentials.SessionToken, 16384) ||
		credentials.CanExpire && !credentials.Expires.After(time.Now()) {
		return result.fail("credentials", connectorError(ErrAuth))
	}
	budget := httpBudget{http: c.http}
	if err := c.instances(ctx, &budget, request, credentials, &result); err != nil {
		return result.fail("instances", err)
	}
	if err := c.securityFindings(ctx, &budget, request, credentials, &result); err != nil {
		return result.fail("findings", err)
	}
	result.Complete = true
	return result, nil
}

func signedRequest(credentials aws.Credentials, service, region string, body []byte) func(*http.Request) error {
	hash := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(hash[:])
	return func(request *http.Request) error {
		err := v4.NewSigner().SignHTTP(request.Context(), credentials, request, payloadHash, service, region, time.Now().UTC())
		if request.Context().Err() != nil {
			return connectorError(request.Context().Err())
		}
		if err != nil {
			return connectorError(ErrAuth)
		}
		return nil
	}
}

func (c *awsCollector) instances(ctx context.Context, budget *httpBudget, request CollectRequest, credentials aws.Credentials, result *Collection) error {
	target := apiURL(c.base, "")
	headers := http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}, "Accept": {"application/xml"}}
	token := ""
	seen := make(map[string]bool)
	for page := 1; page <= c.http.limits.Pages; page++ {
		form := url.Values{
			"Action": {"DescribeInstances"}, "Version": {EC2APIVersion},
			"MaxResults":    {strconv.Itoa(c.http.limits.PageSize)},
			"Filter.1.Name": {"owner-id"}, "Filter.1.Value.1": {request.AccountID},
		}
		if token != "" {
			form.Set("NextToken", token)
		}
		body := []byte(form.Encode())
		response, err := budget.do(ctx, http.MethodPost, target, headers, body, signedRequest(credentials, "ec2", request.Region, body))
		if err != nil {
			return err
		}
		if response.Status != http.StatusOK {
			return connectorError(ErrProtocol)
		}
		records, next, err := ec2Page(ctx, response.Body, request.AccountID, target.String())
		result.Records = append(result.Records, records...)
		if err != nil {
			return err
		}
		if err := continueFeed(result, "instances", next, seen, page, c.http.limits); err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		token = next
	}
	return connectorError(ErrLimit)
}

func ec2Page(ctx context.Context, raw []byte, account, rawURL string) ([]Record, string, error) {
	var envelope struct {
		XMLName        xml.Name  `xml:"DescribeInstancesResponse"`
		NextToken      string    `xml:"nextToken"`
		ReservationSet *struct{} `xml:"reservationSet"`
	}
	if xml.Unmarshal(raw, &envelope) != nil || envelope.ReservationSet == nil ||
		envelope.XMLName.Space != "" && envelope.XMLName.Space != "http://ec2.amazonaws.com/doc/"+EC2APIVersion+"/" {
		return nil, "", connectorError(ErrProtocol)
	}
	reservations, err := xmlRecords(ctx, raw, []string{"DescribeInstancesResponse", "reservationSet", "item"})
	if err != nil {
		return nil, "", err
	}
	var records []Record
	for _, reservation := range reservations {
		var owner struct {
			OwnerID string `xml:"ownerId"`
		}
		if xml.Unmarshal(reservation, &owner) != nil {
			return records, "", connectorError(ErrProtocol)
		}
		if owner.OwnerID != account {
			return records, "", connectorError(ErrScope)
		}
		instances, err := xmlRecords(ctx, reservation, []string{"item", "instancesSet", "item"})
		if err != nil {
			return records, "", err
		}
		for _, instance := range instances {
			var native struct {
				ID    string `xml:"instanceId"`
				State struct {
					Name string `xml:"name"`
				} `xml:"instanceState"`
				Placement struct {
					AvailabilityZone string `xml:"availabilityZone"`
				} `xml:"placement"`
			}
			if xml.Unmarshal(instance, &native) != nil || !instanceID.MatchString(native.ID) {
				return records, "", connectorError(ErrProtocol)
			}
			records = append(records, Record{
				Kind: "resource", ExternalID: native.ID, ParentID: owner.OwnerID,
				State: native.State.Name, Location: native.Placement.AvailabilityZone, Raw: instance, RawURL: rawURL,
			})
		}
	}
	return records, envelope.NextToken, nil
}

// Decoder offsets retain the exact native element bytes, including unknown
// vendor fields and namespace spelling, instead of reserializing XML.
func xmlRecords(ctx context.Context, raw []byte, target []string) ([][]byte, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var stack []string
	var records [][]byte
	roots := 0
	for {
		if ctx.Err() != nil {
			return nil, connectorError(ctx.Err())
		}
		start := decoder.InputOffset()
		token, err := decoder.Token()
		if err == io.EOF {
			if roots != 1 || len(stack) != 0 {
				return nil, connectorError(ErrProtocol)
			}
			return records, nil
		}
		if err != nil {
			return nil, connectorError(ErrProtocol)
		}
		switch token := token.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				roots++
			}
			stack = append(stack, token.Name.Local)
			if len(stack) > 64 || roots != 1 {
				return nil, connectorError(ErrLimit)
			}
			if slices.Equal(stack, target) {
				var discard struct{}
				if decoder.DecodeElement(&discard, &token) != nil {
					return nil, connectorError(ErrProtocol)
				}
				records = append(records, raw[start:decoder.InputOffset()])
				stack = stack[:len(stack)-1]
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, connectorError(ErrProtocol)
			}
			stack = stack[:len(stack)-1]
		case xml.Directive:
			return nil, connectorError(ErrProtocol)
		}
	}
}

type asffFilter struct {
	Value      string `json:"Value"`
	Comparison string `json:"Comparison"`
}

func (c *awsCollector) securityFindings(ctx context.Context, budget *httpBudget, request CollectRequest, credentials aws.Credentials, result *Collection) error {
	target := apiURL(c.findings, "findings")
	headers := http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}}
	token := ""
	seen := make(map[string]bool)
	for page := 1; page <= c.http.limits.Pages; page++ {
		query := struct {
			Filters struct {
				Account []asffFilter `json:"AwsAccountId"`
				Region  []asffFilter `json:"Region"`
			} `json:"Filters"`
			MaxResults int    `json:"MaxResults"`
			NextToken  string `json:"NextToken,omitempty"`
		}{MaxResults: c.http.limits.PageSize, NextToken: token}
		query.Filters.Account = []asffFilter{{Value: request.AccountID, Comparison: "EQUALS"}}
		query.Filters.Region = []asffFilter{{Value: request.Region, Comparison: "EQUALS"}}
		body, err := json.Marshal(query)
		if err != nil {
			return connectorError(ErrProtocol)
		}
		response, err := budget.do(ctx, http.MethodPost, target, headers, body, signedRequest(credentials, "securityhub", request.Region, body))
		if err != nil {
			return err
		}
		var native struct {
			Findings  []json.RawMessage `json:"Findings"`
			NextToken string            `json:"NextToken"`
		}
		if response.Status != http.StatusOK || json.Unmarshal(response.Body, &native) != nil || native.Findings == nil {
			return connectorError(ErrProtocol)
		}
		if len(native.Findings) > c.http.limits.PageSize {
			return connectorError(ErrLimit)
		}
		for _, raw := range native.Findings {
			record, err := asffRecord(raw, request, target.String())
			if err != nil {
				return err
			}
			result.Records = append(result.Records, record)
		}
		if err := continueFeed(result, "findings", native.NextToken, seen, page, c.http.limits); err != nil {
			return err
		}
		if native.NextToken == "" {
			return nil
		}
		token = native.NextToken
	}
	return connectorError(ErrLimit)
}

func asffRecord(raw []byte, request CollectRequest, rawURL string) (Record, error) {
	var native struct {
		SchemaVersion string `json:"SchemaVersion"`
		ID            string `json:"Id"`
		ProductARN    string `json:"ProductArn"`
		AccountID     string `json:"AwsAccountId"`
		Region        string `json:"Region"`
		UpdatedAt     string `json:"UpdatedAt"`
		RecordState   string `json:"RecordState"`
		Severity      struct {
			Label string `json:"Label"`
		} `json:"Severity"`
		Workflow struct {
			Status string `json:"Status"`
		} `json:"Workflow"`
		Resources []struct {
			ID string `json:"Id"`
		} `json:"Resources"`
	}
	if json.Unmarshal(raw, &native) != nil || !textID(native.ID, 2048) || native.SchemaVersion != ASFFVersion {
		return Record{}, connectorError(ErrProtocol)
	}
	product, err := arn.Parse(native.ProductARN)
	if err != nil || product.Partition != "aws" || product.Service != "securityhub" || product.Region != request.Region ||
		!strings.HasPrefix(product.Resource, "product/") || native.AccountID != request.AccountID || native.Region != request.Region {
		return Record{}, connectorError(ErrScope)
	}
	updated, err := sourceTime(native.UpdatedAt)
	if err != nil {
		return Record{}, err
	}
	state := native.Workflow.Status
	if state == "" {
		state = native.RecordState
	}
	location := ""
	if len(native.Resources) > 0 {
		location = native.Resources[0].ID
	}
	return Record{
		Kind: "finding", ExternalID: native.ID, ParentID: native.ProductARN, Severity: native.Severity.Label,
		State: state, Location: location, Raw: raw, RawURL: rawURL, SourceUpdatedAt: updated,
	}, nil
}
