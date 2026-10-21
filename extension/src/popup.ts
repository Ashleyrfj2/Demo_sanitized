interface PopupState {
  config: Record<string, string | boolean>;
  status: {
    state: string;
    pendingCount: number;
    failedCount: number;
    queueFullCount: number;
    lastFailure?: { code: string; error: string; status?: number };
  };
}

const fields = ["tenantId", "buildId", "environmentId", "scenarioId", "actorId", "role", "sessionId", "runId", "apiBaseUrl"];

async function request(message: unknown): Promise<any> { return chrome.runtime.sendMessage(message); }

async function render(): Promise<void> {
  const state = await request({ type: "QA_GET_STATE" }) as PopupState;
  for (const field of fields) {
    const input = document.querySelector<HTMLInputElement | HTMLSelectElement>(`[name=${field}]`);
    if (input) input.value = String(state.config[field] ?? "");
  }
  const enabled = document.querySelector<HTMLInputElement>("[name=captureEnabled]");
  if (enabled) enabled.checked = Boolean(state.config.captureEnabled);
  const status = document.querySelector<HTMLElement>("#status");
  if (status) {
    const failure = state.status.lastFailure;
    const classification = failure
      ? ` · ${failure.code}${failure.status ? ` (${failure.status})` : ""}: ${failure.error}`
      : "";
    status.textContent = `${state.status.state} · pending ${state.status.pendingCount} · failed ${state.status.failedCount} · full ${state.status.queueFullCount}${classification}`;
  }
}

document.querySelector("form")?.addEventListener("submit", (event) => {
  event.preventDefault();
  void (async () => {
    const form = new FormData(event.currentTarget as HTMLFormElement);
    const payload: Record<string, string | boolean> = {};
    for (const field of fields) payload[field] = String(form.get(field) ?? "");
    payload.apiToken = String(form.get("apiToken") ?? "");
    payload.captureEnabled = form.get("captureEnabled") === "on";
    const result = await request({ type: "QA_SAVE_CONFIG", payload });
    const errors = document.querySelector<HTMLElement>("#errors");
    if (errors) errors.textContent = result.saved ? "Saved" : result.code ?? `Check: ${(result.errors ?? []).join(", ")}`;
    await render();
  })();
});

document.querySelector("#retry")?.addEventListener("click", () => { void request({ type: "QA_RETRY_FAILED" }).then(render); });
void render();
