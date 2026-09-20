import type { apiVersion } from "./types";

export const aiFamilies = ["openai", "azure-foundry", "anthropic", "local"] as const;
export const aiPolicyModes = ["disabled", "local-only", "approved-hosted"] as const;
export type AIFamily = typeof aiFamilies[number];
export type AIPolicyMode = typeof aiPolicyModes[number];

export interface AIProfile {
  id: string;
  workspaceId: string;
  name: string;
  family: AIFamily;
  endpoint: string;
  model: string;
  deployment: string;
  enabled: boolean;
  structuredOutput: boolean;
  credentialConfigured: boolean;
  revision: string;
  createdAt: string;
  updatedAt: string;
}

export interface AIProfileInput {
  name: string;
  family: AIFamily;
  endpoint: string;
  model: string;
  deployment: string;
  enabled: boolean;
  structuredOutput: boolean;
  apiKey?: string | null;
}
export type AIProfilePatch = Partial<AIProfileInput>;

export interface AIPolicy {
  workspaceId: string;
  mode: AIPolicyMode;
  revision: string;
  updatedAt: string | null;
  updatedBy: string | null;
}

export interface AIGrantInput {
  profileId: string;
  profileRevision: string;
  policyRevision: string;
  destination: string;
  task: "finding-validity";
  dataClass: "finding-evidence";
  expiresAt: string;
}
export interface AIGrant extends AIGrantInput {
  id: string;
  workspaceId: string;
  createdAt: string;
  grantedBy: string;
  revokedAt: string | null;
  revokedBy: string | null;
}

export interface AIPage<T> {
  apiVersion: typeof apiVersion;
  items: T[];
  total: number;
  nextCursor: string | null;
}
