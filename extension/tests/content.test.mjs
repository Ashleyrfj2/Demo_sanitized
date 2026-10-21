import assert from "node:assert/strict";
import test from "node:test";

const originalNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");

// Mock minimal DOM environment for testing content.ts event handling
function createMockDOM() {
  const listeners = [];
  const sentMessages = [];

  globalThis.document = {
    addEventListener(type, listener, useCapture) {
      if (type === "click") listeners.push({ listener, useCapture });
    },
  };

  globalThis.location = { pathname: "/trips/10000000-0000-4000-8000-000000000001/camp-grid" };
  Object.defineProperty(globalThis, "navigator", {
    configurable: true,
    value: { onLine: true },
  });

  globalThis.chrome = {
    runtime: {
      sendMessage(msg) {
        sentMessages.push(msg);
      },
    },
  };

  function triggerClick(targetElement, eventOverrides = {}) {
    const event = {
      target: targetElement,
      stopPropagationCalled: false,
      stopPropagation() {
        this.stopPropagationCalled = true;
      },
      ...eventOverrides,
    };
    for (const entry of listeners) {
      entry.listener(event);
    }
    return event;
  }

  return { listeners, sentMessages, triggerClick };
}

// Mock Element class hierarchy
class MockNode {
  constructor(tagName, attributes = {}, parent = null) {
    this.tagName = tagName.toUpperCase();
    this.nodeType = 1;
    this.attributes = attributes;
    this.parentElement = parent;
  }

  getAttribute(name) {
    return this.attributes[name] || null;
  }

  closest(selector) {
    const selectors = selector.split(",").map((s) => s.trim().toLowerCase());
    let current = this;
    while (current) {
      const tag = current.tagName.toLowerCase();
      const role = current.getAttribute("role");
      if (selectors.includes(tag) || (role && selectors.includes(`[role='${role}']`))) {
        return current;
      }
      current = current.parentElement;
    }
    return null;
  }
}

globalThis.Element = MockNode;
globalThis.Node = MockNode;
globalThis.HTMLElement = MockNode;
globalThis.HTMLInputElement = class extends MockNode {
  constructor(attributes = {}, parent = null) {
    super("INPUT", attributes, parent);
    this.type = attributes.type || "text";
  }
};

test("content.ts click handling & ancestor resolution", async (t) => {
  t.after(() => {
    if (originalNavigator) {
      Object.defineProperty(globalThis, "navigator", originalNavigator);
    } else {
      delete globalThis.navigator;
    }
  });

  const mockDOM = createMockDOM();

  // Import compiled content.js module
  await import(`../dist/content.js?test=${Date.now()}`);

  assert.equal(mockDOM.listeners.length, 1, "Click listener registered on document");
  assert.equal(mockDOM.listeners[0].useCapture, true, "Click listener registered in capture phase (useCapture: true)");

  // Test 1: Direct click on qualifying button
  const button = new MockNode("BUTTON", { role: "button", "data-testid": "start-btn" });
  const event1 = mockDOM.triggerClick(button);

  assert.equal(mockDOM.sentMessages.length, 1);
  assert.equal(mockDOM.sentMessages[0].type, "QA_CAPTURE_ENVELOPE");
  assert.equal(mockDOM.sentMessages[0].payload.control, "testid:start-btn");

  // Test 1b: Qualifying control captured in capture phase even if target/ancestor later calls stopPropagation()
  event1.stopPropagation();
  assert.equal(mockDOM.sentMessages.length, 1, "Envelope captured before stopPropagation()");
  assert.equal(event1.stopPropagationCalled, true, "stopPropagation was called afterwards by element handler");

  // Test 2: Click on child div inside qualifying button
  mockDOM.sentMessages.length = 0;
  const childDiv = new MockNode("DIV", {}, button);
  mockDOM.triggerClick(childDiv);

  assert.equal(mockDOM.sentMessages.length, 1, "Child click captured as ancestor button");
  assert.equal(mockDOM.sentMessages[0].payload.control, "testid:start-btn", "Normalized info comes from ancestor button");

  // Test 3: The pack toggle gets an exact item/action identity without retaining its user-visible ARIA text.
  mockDOM.sentMessages.length = 0;
  const canopyCard = new MockNode("DIV", {
    "data-testid": "supply-item-10000000-0000-4000-8000-000000000101",
  });
  const packButton = new MockNode("BUTTON", {
    role: "button",
    "aria-label": "Mark Shared Canopy packed",
  }, canopyCard);
  mockDOM.triggerClick(packButton);

  assert.equal(mockDOM.sentMessages.length, 1, "ARIA-labeled pack button captured");
  assert.equal(
    mockDOM.sentMessages[0].payload.control,
    "testid:supply-item-10000000-0000-4000-8000-000000000101:action:mark-packed",
    "Control identity combines the item test id with a normalized action",
  );
  assert.equal(
    mockDOM.sentMessages[0].payload.control.includes("Shared Canopy"),
    false,
    "User-visible item text is not persisted in the control identity",
  );

  // Test 4: Click on unrelated div (no qualifying ancestor)
  mockDOM.sentMessages.length = 0;
  const unrelatedDiv = new MockNode("DIV", {});
  mockDOM.triggerClick(unrelatedDiv);

  assert.equal(mockDOM.sentMessages.length, 0, "Unrelated click ignored");

  // Test 5: Single click produces exactly one capture
  mockDOM.sentMessages.length = 0;
  mockDOM.triggerClick(childDiv);
  assert.equal(mockDOM.sentMessages.length, 1, "Exactly one envelope per click");

  // Test 6: Password and email input exclusions remain intact
  mockDOM.sentMessages.length = 0;
  const passwordInput = new globalThis.HTMLInputElement({ type: "password", id: "user-pass" });
  mockDOM.triggerClick(passwordInput);
  assert.equal(mockDOM.sentMessages.length, 0, "Password input click ignored");

  mockDOM.sentMessages.length = 0;
  const emailInput = new globalThis.HTMLInputElement({ type: "email", id: "user-email" });
  mockDOM.triggerClick(emailInput);
  assert.equal(mockDOM.sentMessages.length, 0, "Email input click ignored");
});
