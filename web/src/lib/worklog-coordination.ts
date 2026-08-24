export const IDENTITY_DRAFT_TAG = "identity-draft";
const IDENTITY_DRAFT_RECIPIENT_PREFIX = "to:";
const IDENTITY_DRAFT_THREAD_PREFIX = "thread:";

export type WorkLogCoordinationDraft = {
  recipients: string[];
  thread?: string;
  userTags: string[];
};

type TaggedWorkLog = {
  tags?: string[];
  coordination?: {
    version?: number;
    recipients?: string[];
    thread?: string;
  };
};

/** Parse the reserved, append-only Agent coordination envelope from Work Log tags. */
export function workLogCoordinationDraft(log: TaggedWorkLog): WorkLogCoordinationDraft | undefined {
  const tags = log.tags ?? [];
  const envelope = log.coordination;
  // Authorization and UI classification both rely on this server-owned versioned
  // envelope. Historical user tags can never promote a private log into a draft.
  if (envelope?.version !== 1) return undefined;
  return {
    recipients: [...new Set((envelope.recipients ?? []).filter(Boolean))],
    thread: envelope.thread || undefined,
    userTags: tags.filter((tag) => tag !== IDENTITY_DRAFT_TAG
      && !tag.startsWith(IDENTITY_DRAFT_RECIPIENT_PREFIX)
      && !tag.startsWith(IDENTITY_DRAFT_THREAD_PREFIX)),
  };
}

export function isWorkLogCoordinationDraft(log: TaggedWorkLog): boolean {
  return Boolean(workLogCoordinationDraft(log));
}
