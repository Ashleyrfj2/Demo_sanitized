interface LocalCaptureEnvelope {
  actionType: string;
  route: string;
  control: string;
  occurredAt: string;
  online: boolean;
  redactedFields: string[];
}

const blockedRoute = /^\/auth(?:\/|$)|safety|emergency/i;
document.documentElement?.setAttribute("data-qa-evidence-capture", "ready");

function nearestTestId(target: HTMLElement): string | null {
  let current: HTMLElement | null = target;
  while (current) {
    const testId = current.getAttribute("data-testid");
    if (testId) return testId.slice(0, 80);
    current = current.parentElement;
  }
  return null;
}

function safeAriaAction(target: HTMLElement): string | null {
  const ariaLabel = target.getAttribute("aria-label")?.trim();
  if (!ariaLabel) return null;
  if (/^Mark .+ unpacked$/i.test(ariaLabel)) return "mark-unpacked";
  if (/^Mark .+ packed$/i.test(ariaLabel)) return "mark-packed";
  return null;
}

function stableControlIdentity(target: HTMLElement): string {
  const testId = target.getAttribute("data-testid");
  if (testId) return `testid:${testId.slice(0, 80)}`;

  const ariaAction = safeAriaAction(target);
  const ancestorTestId = nearestTestId(target.parentElement ?? target);
  if (ariaAction && ancestorTestId) {
    return `testid:${ancestorTestId}:action:${ariaAction}`.slice(0, 80);
  }

  const elementId = target.id;
  if (elementId && !/(email|password|token|secret)/i.test(elementId)) return `id:${elementId.slice(0, 80)}`;
  const role = target.getAttribute("role") || target.tagName.toLowerCase();
  const type = target instanceof HTMLInputElement ? target.type : "control";
  return `${role}:${type}`.slice(0, 80);
}

document.addEventListener("click", (event) => {
  if (blockedRoute.test(location.pathname)) return;
  const rawTarget = event.target instanceof Element ? event.target : (event.target instanceof Node ? event.target.parentElement : null);
  const target = rawTarget ? rawTarget.closest<HTMLElement>("button,a,[role='button'],input") : null;
  if (!target || (target instanceof HTMLInputElement && ["password", "email"].includes(target.type))) return;
  const envelope: LocalCaptureEnvelope = {
    actionType: "control_activated",
    route: location.pathname.slice(0, 240),
    control: stableControlIdentity(target),
    occurredAt: new Date().toISOString(),
    online: navigator.onLine,
    redactedFields: ["control.text", "request.body", "auth", "cookies"],
  };
  chrome.runtime.sendMessage({ type: "QA_CAPTURE_ENVELOPE", payload: envelope });
}, true);