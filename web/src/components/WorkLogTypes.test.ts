/// <reference types="node" />

import assert from "node:assert/strict";
import test from "node:test";
import {
  isWorkLogCoordinationDraft,
  workLogCoordinationDraft,
} from "../lib/worklog-coordination.ts";

test("coordination drafts require the server-owned versioned envelope", () => {
  assert.equal(isWorkLogCoordinationDraft({ tags: ["identity-draft"] }), false);
  assert.equal(isWorkLogCoordinationDraft({ tags: ["identity-draft"], coordination: { version: 1 } }), true);
  assert.equal(isWorkLogCoordinationDraft({ tags: ["identity-draft"], coordination: { version: 2 } }), false);
  assert.equal(isWorkLogCoordinationDraft({ tags: undefined }), false);
});

test("coordination envelope separates recipients, thread, and user tags", () => {
  assert.deepEqual(workLogCoordinationDraft({
    tags: [
      "identity-draft",
      "to:agent:frontend",
      "to:agent:backend",
      "to:agent:frontend",
      "thread:handoff_42",
      "architecture",
    ],
    coordination: {
      version: 1,
      recipients: ["agent:frontend", "agent:backend", "agent:frontend"],
      thread: "handoff_42",
    },
  }), {
    recipients: ["agent:frontend", "agent:backend"],
    thread: "handoff_42",
    userTags: ["architecture"],
  });
});

test("coordination draft without recipients remains a project broadcast", () => {
  assert.deepEqual(workLogCoordinationDraft({ tags: ["identity-draft", "decision"], coordination: { version: 1 } }), {
    recipients: [],
    thread: undefined,
    userTags: ["decision"],
  });
});
