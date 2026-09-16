package parsers

func parseTrivy(root any) ([]Finding, error) {
	report := obj(root)
	version, _ := scalar(report["SchemaVersion"])
	results, ok := array(report["Results"])
	if version != "2" || !ok {
		return nil, ErrInvalid
	}
	findings := []Finding{}
	for _, rawResult := range results {
		result := obj(rawResult)
		target := str(result["Target"])
		rawItems, exists := result["Vulnerabilities"]
		items, ok := array(rawItems)
		if !exists || (rawItems != nil && !ok) || target == "" || len(findings)+len(items) > maxFindings {
			return nil, ErrInvalid
		}
		for _, rawItem := range items {
			item := obj(rawItem)
			nativeID, description := str(item["VulnerabilityID"]), str(item["Description"])
			remediation := ""
			if fixed := str(item["FixedVersion"]); fixed != "" {
				remediation = "Update " + str(item["PkgName"]) + " to a fixed version: " + fixed
			}
			findings = append(findings, Finding{
				Identity:        identity("trivy", nativeID, str(item["PkgName"]), target),
				SourceFindingID: nativeID, Title: first(str(item["Title"]), nativeID),
				Description: description, Impact: description, Remediation: remediation,
				SourceSeverity: str(item["Severity"]), Severity: severity(str(item["Severity"])),
				Location: Location{URI: target}, EvidenceText: description, SourceLabel: "Trivy report",
				Unmapped: remaining(item, "VulnerabilityID", "Title", "Description", "Severity"),
			})
		}
	}
	return findings, nil
}

func parseZAP(root any) ([]Finding, error) {
	report := obj(root)
	sites, ok := array(report["site"])
	if str(report["@version"]) != "2.16.1" || !ok {
		return nil, ErrInvalid
	}
	findings := []Finding{}
	for _, rawSite := range sites {
		site := obj(rawSite)
		alerts, ok := array(site["alerts"])
		if !ok {
			return nil, ErrInvalid
		}
		for _, rawAlert := range alerts {
			alert := obj(rawAlert)
			instances, ok := array(alert["instances"])
			if !ok || len(instances) == 0 || len(findings)+len(instances) > maxFindings {
				return nil, ErrInvalid
			}
			nativeID, description := str(alert["alertRef"]), str(alert["desc"])
			rawSeverity, ok := scalar(alert["riskcode"])
			if !ok {
				return nil, ErrInvalid
			}
			for _, rawInstance := range instances {
				instance := obj(rawInstance)
				uri := str(instance["uri"])
				if uri == "" {
					return nil, ErrInvalid
				}
				unmapped := remaining(alert, "alertRef", "alert", "riskcode", "desc", "solution", "instances")
				unmapped["instances"] = []any{instance}
				findings = append(findings, Finding{
					Identity: identity("zap", nativeID, uri), SourceFindingID: nativeID,
					Title: str(alert["alert"]), Description: description, Impact: description,
					SourceSeverity: rawSeverity, Severity: severity(rawSeverity),
					Remediation: str(alert["solution"]), Location: Location{URI: uri},
					EvidenceText: first(str(instance["evidence"]), description), SourceLabel: "ZAP report",
					Unmapped: unmapped,
				})
			}
		}
	}
	return findings, nil
}

func parseGitleaks(root any) ([]Finding, error) {
	items, ok := array(root)
	if !ok || len(items) > maxFindings {
		return nil, ErrInvalid
	}
	findings := make([]Finding, 0, len(items))
	for _, rawItem := range items {
		item := obj(rawItem)
		line, err := sourceLine(item["StartLine"])
		if item == nil || err != nil {
			return nil, ErrInvalid
		}
		nativeID, description := str(item["Fingerprint"]), str(item["Description"])
		findings = append(findings, Finding{
			Identity: identity("gitleaks", nativeID), SourceFindingID: nativeID,
			Title: description, Description: description, Impact: description,
			Severity: "high", SourceSeverity: "",
			Location:     Location{URI: str(item["File"]), Line: line},
			EvidenceText: description, SourceLabel: "Gitleaks report",
			Unmapped: remaining(item, "Fingerprint", "Description", "File", "StartLine"),
		})
	}
	return findings, nil
}
