export const apiVersion = "aspm/v1alpha1" as const;

export interface WorkItem {
  id: string;
  title: string;
  assetName: string;
  severity: "critical" | "high" | "medium" | "low" | "info";
  ownerName: string | null;
  workflowState: "open" | "in-progress" | "resolved";
  sourceScanAt: string | null;
  collectedAt: string;
  importedAt: string;
}

export interface WorkResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: "synthetic" | "live";
  items: WorkItem[];
  total: number;
  nextCursor: string | null;
}

export interface FindingResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: "synthetic" | "live";
  finding: WorkItem & {
    scopeLabel: string;
    description: string;
    evidence: {
      text: string;
      sourceLabel: string;
      verificationState: "not-run" | "blocked" | "verified";
    };
    remediation: string;
  };
}

export interface IntegrationSummary {
  id: "github" | "gitlab" | "azure-devops" | "aws" | "azure" | "jira" | "teams" | "slack";
  name: string;
  kind: "native";
  supportMaturity: "planned" | "experimental" | "supported";
  connectionState: "unconfigured" | "healthy" | "stale" | "failed" | "partially-authorized";
  readyToConnect: boolean;
  liveVerification: {
    state: "not-run" | "blocked" | "passed" | "failed";
    reason: string;
  };
  capabilities: string[];
}

export interface CatalogResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: "synthetic" | "live";
  items: IntegrationSummary[];
}

export interface ErrorResponse {
  apiVersion: typeof apiVersion;
  error: {
    code: "unavailable" | "forbidden" | "not-found";
    message: string;
    requestId: string;
    retryable: boolean;
  };
}
