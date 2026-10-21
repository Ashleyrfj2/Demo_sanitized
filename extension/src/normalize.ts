import type { CaptureConfig } from "./config.js";

export interface CaptureEnvelope {
  actionType: string;
  route: string;
  control: string;
  occurredAt: string;
  online: boolean;
  redactedFields: string[];
}

export interface ValidationEvent {
  event_id: string;
  tenant_id: string;
  session_id: string;
  run_id: string;
  actor_id: string;
  source_type: "human";
  source_adapter: "chrome-mv3";
  build_id: string;
  environment_id: string;
  context: Record<string, string>;
  route: string;
  state_fingerprint: string;
  action_type: string;
  outcome: "ambiguous";
  occurred_at: string;
  step_index: number;
  privacy: { masked: true; redacted_fields: string[] };
}

export function normalize(envelope: CaptureEnvelope, config: CaptureConfig, stepIndex: number): ValidationEvent {
  return {
    event_id: crypto.randomUUID(),
    tenant_id: config.tenantId,
    session_id: config.sessionId,
    run_id: config.runId,
    actor_id: config.actorId,
    source_type: "human",
    source_adapter: "chrome-mv3",
    build_id: config.buildId,
    environment_id: config.environmentId,
    context: {
      role: config.role,
      browser: "chrome",
      scenario_id: config.scenarioId,
      control: envelope.control,
      connectivity: envelope.online ? "online" : "offline",
    },
    route: envelope.route,
    state_fingerprint: `route:${envelope.route}:control:${envelope.control}`,
    action_type: envelope.actionType,
    outcome: "ambiguous",
    occurred_at: envelope.occurredAt,
    step_index: stepIndex,
    privacy: { masked: true, redacted_fields: envelope.redactedFields },
  };
}
