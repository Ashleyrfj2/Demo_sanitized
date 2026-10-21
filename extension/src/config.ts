export interface CaptureConfig {
  tenantId: string;
  buildId: string;
  environmentId: string;
  scenarioId: string;
  actorId: string;
  role: string;
  sessionId: string;
  runId: string;
  apiBaseUrl: string;
  apiToken: string;
  captureEnabled: boolean;
}

const defaults: CaptureConfig = {
  tenantId: "festival-thesis-demo",
  buildId: "festnest-demo-001",
  environmentId: "festnest-local-browser",
  scenarioId: "equipment-handoff-v1",
  actorId: "",
  role: "",
  sessionId: "",
  runId: "",
  apiBaseUrl: "http://127.0.0.1:8080",
  apiToken: "",
  captureEnabled: false,
};

function isAllowedAPIBaseURL(rawURL: string): boolean {
  try {
    const api = new URL(rawURL);
    return api.protocol === "http:" && api.port === "8080" &&
      (api.hostname === "127.0.0.1" || api.hostname === "localhost");
  } catch {
    return false;
  }
}

export async function loadConfig(): Promise<CaptureConfig> {
  const stored = await chrome.storage.local.get("captureConfig");
  const config = { ...defaults, ...(stored.captureConfig ?? {}) } as CaptureConfig;
  if (!isAllowedAPIBaseURL(config.apiBaseUrl)) {
    // Older or externally modified storage must fail closed without making the
    // popup unusable. Never let a queued event inherit an invalid destination.
    return { ...config, apiBaseUrl: defaults.apiBaseUrl, captureEnabled: false };
  }
  return config;
}

export function configIsComplete(config: CaptureConfig): boolean {
  return Boolean(
    config.captureEnabled && config.tenantId && config.buildId && config.environmentId &&
    config.scenarioId && config.actorId && ["leader", "editor", "viewer"].includes(config.role) &&
    config.sessionId && config.runId && config.apiToken
  );
}

export const identityConfigFields: Array<keyof CaptureConfig> = [
  "tenantId", "buildId", "environmentId", "scenarioId", "actorId", "role", "sessionId", "runId",
];

export function validateConfig(config: CaptureConfig): string[] {
  const errors: string[] = [];
  if (!isAllowedAPIBaseURL(config.apiBaseUrl)) errors.push("apiBaseUrl");
  for (const field of identityConfigFields) {
    if (!String(config[field] ?? "").trim()) errors.push(field);
  }
  if (!["leader", "editor", "viewer"].includes(config.role)) errors.push("role");
  if (!config.apiToken.trim()) errors.push("apiToken");
  return [...new Set(errors)];
}
