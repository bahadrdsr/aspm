package parsers

import (
	"encoding/json"
	"strconv"
)

func parseSARIF(root any) ([]Finding, error) {
	report := obj(root)
	if str(report["version"]) != "2.1.0" {
		return nil, ErrInvalid
	}
	runs, ok := array(report["runs"])
	if !ok {
		return nil, ErrInvalid
	}
	findings := []Finding{}
	for _, rawRun := range runs {
		run := obj(rawRun)
		driver := obj(obj(run["tool"])["driver"])
		sourceLabel := str(driver["name"])
		results, ok := array(run["results"])
		if !ok || sourceLabel == "" || len(findings)+len(results) > maxFindings {
			return nil, ErrInvalid
		}
		rules := make(map[string]map[string]any)
		ruleList, _ := array(driver["rules"])
		for _, rawRule := range ruleList {
			rule := obj(rawRule)
			id := str(rule["id"])
			if id == "" || rules[id] != nil {
				return nil, ErrInvalid
			}
			rules[id] = rule
		}
		for _, rawResult := range results {
			result := obj(rawResult)
			ruleID := str(result["ruleId"])
			rule := rules[ruleID]
			message := text(result["message"])
			if result == nil || ruleID == "" || message == "" {
				return nil, ErrInvalid
			}
			var location Location
			locations, _ := array(result["locations"])
			if len(locations) > 0 {
				physical := obj(obj(locations[0])["physicalLocation"])
				location.URI = str(obj(physical["artifactLocation"])["uri"])
				var err error
				location.Line, err = sourceLine(obj(physical["region"])["startLine"])
				if err != nil {
					return nil, err
				}
			}
			sourceID := str(result["guid"])
			key := identity("sarif-guid", sourceID)
			if sourceID == "" {
				fingerprints := obj(result["partialFingerprints"])
				if len(fingerprints) == 0 {
					fingerprints = obj(result["fingerprints"])
				}
				sourceID = ruleID
				if len(fingerprints) > 0 {
					encoded, _ := json.Marshal(fingerprints)
					key = identity("sarif-fingerprint", ruleID, string(encoded))
				} else {
					key = identity("sarif-location", ruleID, location.URI, strconv.Itoa(location.Line))
				}
			}
			properties := obj(result["properties"])
			unmapped := remaining(result, "guid", "level", "message")
			for name, value := range properties {
				if name != "impact" {
					if _, present := unmapped[name]; !present {
						unmapped[name] = value
					}
				}
			}
			rawSeverity := str(result["level"])
			normalized := severity(first(rawSeverity, str(obj(rule["defaultConfiguration"])["level"]), "warning"))
			findings = append(findings, Finding{
				Identity: key, SourceFindingID: sourceID,
				Title:          first(text(rule["shortDescription"]), message, ruleID),
				Description:    first(text(rule["fullDescription"]), message),
				SourceSeverity: rawSeverity, Severity: normalized,
				Location: location, Impact: str(properties["impact"]),
				Remediation: text(rule["help"]), EvidenceText: message,
				SourceLabel: sourceLabel, Unmapped: unmapped,
			})
		}
	}
	return findings, nil
}
