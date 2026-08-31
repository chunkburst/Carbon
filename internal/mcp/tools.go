package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"carbon/internal/compat"
	"carbon/internal/home"
	"carbon/internal/incident"
	"carbon/internal/lease"
	"carbon/internal/review"
	"carbon/internal/search"
	"carbon/internal/stats"
	"carbon/internal/store"
	"carbon/internal/subscription"
	"carbon/internal/task"
	"carbon/internal/templates"
	"carbon/internal/trash"
	tasktypes "carbon/internal/types"
	"carbon/internal/views"
)

var (
	errLeaseClaimExpectedVersionRequired = errors.New("lease_claim requires expected_version")
	errEvidenceExpectedVersionRequired   = errors.New("evidence operations require expected_version")
)

// serverInstructions is returned from the MCP initialize handshake. Keep it short
// enough for clients to retain, but explicit about the stable v2 safety contract
// that a tool catalog alone cannot express.
const serverInstructions = `Use identity first. It returns this connection's fixed actor, resolved scope, compatibility contract, and (when bindingMode=session) selectionVersion. A fixed/pinned connection never changes scope: create_project only creates catalog metadata there, so reconnect with another --project or use a Project Session when work must move. A Carbon Project Session starts at a Home catalog with no active project: use select_project (project required, cluster optional) to choose one project; create_project automatically selects its successful result only in Session mode. Until selection, every task, session, and Work Log tool returns active-project-required and never falls back to the Home directory.

Carbon v2 is the approved stable layer. A project-bound connection reads and writes only its bound project by default; include_cluster=true is an explicit same-cluster read expansion and never expands writes or crosses clusters. Read the current record before a write and pass its raw version or quoted ETag as expected_version whenever a tool marks it required; stale writes are rejected.

Event subscriptions are selected-project, Agent-owned delivery preferences, not project configuration. Use subscription_initialize for task result events and Incident process events, then retain the returned cursor and use events_poll after restart. passive, mixed, and active currently all return effectiveDelivery=poll with pushSupported=false: this MCP session has no verified automatic-wake transport. A cursor is bound to its project, actor, and subscription; after a project switch or retention expiry, explicitly reinitialize with a new idempotency key.

set_blocker, add_evidence, remove_evidence, and lease_claim require expected_version. Carbon v2 ownership uses lease_claim with a non-empty reason and current expected_version; the historical claim tool is registered only for frozen legacy v1. When a project enables Identity Mode, claim a worker identity with allowed task types before lease_claim or begin on a typed task; human/system actors and older untyped tasks remain compatible. Blocker text remains until explicitly cleared. Evidence is a server-audited task proof. Work Logs are separate durable Worker records: worker_private is isolated to its Worker, project_public follows the bound project/cluster scope, and global_public is readable only within the same Carbon home. Identity Mode additionally permits append-only worklog_draft_send server-owned identity-draft coordination records in the same project: named recipients are an exact Agent allow-list, while an empty recipient list broadcasts to that project; ordinary worker_private logs never become visible to peers.

v1 is the frozen LegacyLayer for explicit --repo workspaces. It keeps the historical task/session surface and does not gain Carbon catalog, lease, Work Log, or other v2 tools.`

// NewServer builds the established fixed-binding MCP server over the given Service.
// The actor identity and project scope are baked into svc and never change for the
// life of this server. Use NewProjectSessionServer only when an adapter explicitly
// requested the v2 selectable-project session mode.
func NewServer(svc *Service) *mcpsdk.Server {
	return newServer(svc, nil)
}

// NewProjectSessionServer builds a Carbon v2 MCP server whose catalog and task
// tool names remain stable while select_project changes the active project inside
// one fixed Home. A session starts with no active project, so task/session/Work
// Log calls return ErrActiveProjectRequired until select_project or create_project
// succeeds. Fixed NewServer connections intentionally never register select_project.
func NewProjectSessionServer(session *ProjectSession) *mcpsdk.Server {
	if session == nil {
		panic("nil Carbon ProjectSession")
	}
	return newServer(session.catalogService(), session)
}

// newServer has one lexical svc variable shared by every typed tool closure. For
// fixed bindings it is never changed. For a ProjectSession, its receiving
// middleware serializes an entire tools/call, assigns svc to that call's immutable
// active Service, and only then invokes the handler. A successful select_project
// atomically swaps the session's Service while holding the same mutex.
func newServer(svc *Service, projectSession *ProjectSession) *mcpsdk.Server {
	if svc == nil {
		panic("nil Carbon MCP Service")
	}
	sessionMode := projectSession != nil
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "carbon", Version: ServiceVersion}, &mcpsdk.ServerOptions{
		Instructions: serverInstructions,
	})
	if sessionMode {
		srv.AddReceivingMiddleware(projectSession.serverMiddleware(func(bound *Service) {
			svc = bound
		}))
	}

	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "identity",
		Description: "Return this connection's fixed actor/client, resolved legacy-or-Carbon scope, and canonical compatibility contract. Call first before selecting work, beginning sessions, or writing."},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, Identity, error) {
			if projectSession != nil {
				return nil, projectSession.identityLocked(), nil
			}
			return nil, svc.Identity(), nil
		})

	// Compatibility is a capability boundary, not just identity metadata. A
	// malformed direct Service construction gets only the read-only identity tool;
	// normal CLI and HTTP transports reject it before a listener/store initializer.
	contract, err := svc.Compatibility()
	if err != nil {
		return srv
	}
	carbonCatalog := svc.scope.IsCarbonHome() && contract.RequestedCompatLayer == compat.StableLayer
	taskScope := svc.scope.Legacy || svc.scope.IsCarbon() || sessionMode
	legacyTasks := svc.scope.Legacy && contract.RequestedCompatLayer == compat.LegacyLayer
	carbonStableTasks := (svc.scope.IsCarbon() || sessionMode) && contract.RequestedCompatLayer == compat.StableLayer

	// Carbon home catalog tools deliberately use distinct names from task list/get/create.
	// They resolve a user-facing reference once and return canonical IDs, so an agent can
	// discover a project then bind subsequent MCP work to stable identifiers.
	if carbonCatalog {
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "list_clusters",
			Description: "Carbon v2 stable read: list clusters in the bound home. Returns canonical IDs, slugs, aliases, descriptions, and project counts; it never mutates metadata and is unavailable to legacy --repo."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, clustersOut, error) {
				clusters, err := svc.ListCatalogClusters()
				return nil, clustersOut{Clusters: clusters}, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "get_cluster",
			Description: "Carbon v2 stable read: resolve one cluster. reference is required and is matched by stable ID, then case-insensitive slug/alias, then unique display name. Save the returned canonical_id for later writes; no metadata is changed."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in catalogReferenceIn) (*mcpsdk.CallToolResult, clusterOut, error) {
				cluster, err := svc.ResolveCatalogCluster(in.Reference)
				return nil, clusterOut{Cluster: cluster}, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "resolve_cluster",
			Description: "Carbon v2 stable read: resolve a cluster reference to its canonical stable ID. reference is required; ID wins over slug/alias, then a unique display name. Ambiguous names fail and no metadata is changed."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in catalogReferenceIn) (*mcpsdk.CallToolResult, clusterOut, error) {
				cluster, err := svc.ResolveCatalogCluster(in.Reference)
				return nil, clusterOut{Cluster: cluster}, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "describe_cluster",
			Description: "Carbon v2 stable read: describe one cluster and its project surfaces. reference is required; the result contains canonical IDs and non-task metadata only, with no side effect."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in catalogReferenceIn) (*mcpsdk.CallToolResult, ClusterDescription, error) {
				out, err := svc.DescribeCatalogCluster(in.Reference)
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "create_cluster",
			Description: "Carbon v2 stable write: create a durable cluster only when name, allow_create=true, and a non-empty reason are supplied. This initializes home metadata when needed and returns a generated canonical_id; never infer creation from a failed lookup."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in createCatalogClusterIn) (*mcpsdk.CallToolResult, clusterOut, error) {
				cluster, err := svc.CreateCatalogCluster(CatalogCreateClusterInput{
					Name: in.Name, Slug: in.Slug, Description: in.Description, SlugAliases: in.SlugAliases,
					Prefix: in.Prefix, AllowCreate: in.AllowCreate, Reason: in.Reason,
				})
				return nil, clusterOut{Cluster: cluster}, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "list_projects",
			Description: "Carbon v2 stable read: list projects in the bound home. Pass cluster to restrict to one shared cluster; omit it to discover both shared-cluster and standalone projects. Standalone entries have standalone=true and no cluster id. No metadata is changed."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in listCatalogProjectsIn) (*mcpsdk.CallToolResult, projectsOut, error) {
				projects, err := svc.ListCatalogProjects(in.Cluster)
				return nil, projectsOut{Projects: projects}, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "get_project",
			Description: "Carbon v2 stable read: resolve one project. cluster is optional: omit it for a global standalone-or-cluster lookup, which fails closed if a slug/name is ambiguous. Supplying cluster restricts matching to that shared cluster. Returns canonical IDs without mutation."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in catalogProjectReferenceIn) (*mcpsdk.CallToolResult, ProjectDescription, error) {
				out, err := svc.ResolveCatalogProject(in.Cluster, in.Project)
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "resolve_project",
			Description: "Carbon v2 stable read: resolve a project reference to canonical IDs. cluster is optional; an unscoped ambiguous reference fails rather than selecting a sibling. It reads metadata only; execution paths verify the source fingerprint again before checks or sessions."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in catalogProjectReferenceIn) (*mcpsdk.CallToolResult, ProjectDescription, error) {
				out, err := svc.ResolveCatalogProject(in.Cluster, in.Project)
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "describe_project",
			Description: "Carbon v2 stable read: describe one project. cluster is optional; an unscoped ambiguous reference fails closed. The result includes standalone=true for a private top-level project, otherwise its parent cluster IDs, without mutation."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in catalogProjectReferenceIn) (*mcpsdk.CallToolResult, ProjectDescription, error) {
				out, err := svc.DescribeCatalogProject(in.Cluster, in.Project)
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "create_project",
			Description: "Carbon v2 stable write: create a source-bound standalone project by default, only with name, allow_create=true, a non-empty reason, and an existing local source_path. Supply cluster to create a project surface in that shared pool instead. This mutates catalog metadata and returns canonical IDs. In Project Session mode, a successful create becomes the active project for subsequent task tools. A fixed/pinned connection never changes binding after creation: reconnect with the new --project or use a Project Session to switch."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in createCatalogProjectIn) (*mcpsdk.CallToolResult, ProjectDescription, error) {
				input := CatalogCreateProjectInput{
					Cluster: in.Cluster, Name: in.Name, Slug: in.Slug, Description: in.Description,
					SlugAliases: in.SlugAliases, Kind: in.Kind, SourcePath: in.SourcePath,
					AllowCreate: in.AllowCreate, Reason: in.Reason,
				}
				if projectSession != nil {
					out, _, err := projectSession.createProjectLocked(input)
					// The Project Session middleware holds the same mutex for this
					// handler, so switching the captured service is atomic with the
					// catalog mutation and visible to the next tool call only.
					svc = projectSession.currentLocked()
					return nil, out, err
				}
				out, err := svc.CreateCatalogProject(input)
				return nil, out, err
			})

		if projectSession != nil {
			mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "select_project",
				Description: "Carbon v2 Project Session write: switch this MCP connection's active project within its fixed Home. project is required; cluster is optional for a standalone project and otherwise resolves the shared-cluster parent. The selection is atomic for this connection, returns canonical scope metadata, and never changes the fixed actor."},
				func(_ context.Context, _ *mcpsdk.CallToolRequest, in selectProjectIn) (*mcpsdk.CallToolResult, ProjectSessionSelection, error) {
					out, err := projectSession.selectProjectLocked(in.Cluster, in.Project)
					if err == nil {
						svc = projectSession.currentLocked()
					}
					return nil, out, err
				})
		}
	}

	// A home-only catalog service intentionally has no task store surface. Legacy
	// v1 and a selected Carbon v2 cluster retain their shared task tools; the
	// historical assignment-only claim is separately registered for legacy v1.
	if taskScope {
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "list",
			Description: "Read task summaries, optionally filtered by status, assignee, readiness, or execution state. It has no side effect; ready=true with status=<initial> answers 'what can I start now'. In Carbon v2, include_cluster=true is an explicit same-cluster read expansion."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in listIn) (*mcpsdk.CallToolResult, listOut, error) {
				views, err := svc.ListScoped(in.Status, in.Assignee, in.Ready, in.Execution, in.IncludeCluster)
				if err != nil {
					return nil, listOut{}, err
				}
				out := listOut{Tasks: make([]taskOut, 0, len(views))}
				for _, v := range views {
					out.Tasks = append(out.Tasks, taskOut{ID: v.ID, Title: v.Title, Status: v.Status,
						Assignee: v.Assignee, ProjectID: v.ProjectID, Type: v.Type, Importance: v.Importance, Version: v.Version,
						Deps: v.Deps, Ready: v.Ready, UpdatedAt: v.UpdatedAt, Rank: v.Rank,
						Labels: v.Labels, Priority: v.Priority, Parent: v.Parent, ActiveAttempt: v.ActiveAttempt,
						Lease: v.Lease, PendingClaims: v.PendingClaims, ExecutionState: v.ExecutionState, SessionID: v.SessionID,
						ActivityHealth: v.Health, LastMeaningfulAt: v.LastMeaningfulAt, StagnantAt: v.StagnantAt,
						BlockerReason: v.BlockerReason, Evidence: v.Evidence, Checks: toCheckOut(v.Checks)})
				}
				return nil, out, nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "get",
			Description: "Read one task's fields, checks/results, provenance, and body. id is required; it has no side effect, and in Carbon v2 include_cluster=true is an explicit same-cluster read expansion."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in getIn) (*mcpsdk.CallToolResult, taskOut, error) {
				doc, err := svc.GetScoped(in.ID, in.IncludeCluster)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "create",
			Description: "Create a durable task. title is required; the engine assigns the ID and initial status. Carbon v2 defaults to its bound project and requires explicit type/importance; cluster-wide work requires an explicitly selected cluster scope plus project_id set to an empty string. Dependencies must already exist."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in createIn) (*mcpsdk.CallToolResult, taskOut, error) {
				draft := store.Draft{
					Title: in.Title, Body: in.Body, Deps: in.Deps, Checks: fromCheckIn(in.Checks),
					Labels: in.Labels, Priority: in.Priority, Parent: in.Parent, Type: in.Type, Importance: in.Importance,
				}
				if in.ProjectID != nil {
					draft.ProjectID = *in.ProjectID
					draft.ProjectIDSet = true
				}
				doc, err := svc.CreateContext(ctx, draft)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		if carbonStableTasks {
			mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "list_types",
				Description: "Carbon v2 stable read: list built-in and configured custom task types. It has no side effect; prefer an existing type before creating a durable custom type."},
				func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, taskTypesOut, error) {
					keys, custom, err := svc.ListTaskTypes()
					return nil, taskTypesOut{Types: keys, Custom: custom}, err
				})

			mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "create_type",
				Description: "Carbon v2 stable write: create one reusable custom task type. key is required; this durable mutation is quota- and rate-limited, so use it only when no built-in or existing type fits."},
				func(ctx context.Context, _ *mcpsdk.CallToolRequest, in createTypeIn) (*mcpsdk.CallToolResult, taskTypeOut, error) {
					definition, err := svc.CreateTaskType(ctx, in.Key)
					return nil, taskTypeOut{Definition: definition}, err
				})
		}

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "reorder",
			Description: "Set one task's manual Kanban ordering rank. id and rank are required; this durable write changes board order only and does not append provenance."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in reorderIn) (*mcpsdk.CallToolResult, taskOut, error) {
				doc, err := svc.Reorder(in.ID, in.Rank)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "update",
			Description: "Edit a task: title, body, checks, priority, labels, deps, parent, type, or importance. This mutates the task; expected_version is optional optimistic concurrency. Omitted fields stay unchanged, empty deps clears dependencies, and replacement deps reject missing tasks/cycles. In Carbon v2, project_id and assignee are rejected here: use bulk_move and lease_reassign."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in updateIn) (*mcpsdk.CallToolResult, taskOut, error) {
				f := UpdateFields{Priority: in.Priority, Labels: in.Labels, Deps: in.Deps, Parent: in.Parent, Title: in.Title, Body: in.Body,
					ProjectID: in.ProjectID, Type: in.Type, Importance: in.Importance, Assignee: in.Assignee}
				if in.Checks != nil {
					checks := fromCheckIn(*in.Checks)
					f.Checks = &checks
				}
				doc, err := svc.UpdateWithVersion(in.ID, f, in.ExpectedVersion)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "delete",
			Description: "Move one task to recoverable trash. id is required; it refuses when other tasks name it as parent or dependency, so reparent/remove those references first. This write never permanently empties trash."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in idIn) (*mcpsdk.CallToolResult, deleteOut, error) {
				if err := svc.Delete(in.ID); err != nil {
					return nil, deleteOut{}, err
				}
				return nil, deleteOut{ID: in.ID, Deleted: true}, nil
			})

		if legacyTasks {
			mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "claim",
				Description: "Frozen legacy v1 write: claim a task for this connection's fixed actor. id is required and another owner causes failure. Carbon v2 does not register this tool; use version-protected lease_claim with a non-empty reason instead."},
				func(_ context.Context, _ *mcpsdk.CallToolRequest, in idIn) (*mcpsdk.CallToolResult, taskOut, error) {
					doc, err := svc.Claim(in.ID)
					if err != nil {
						return nil, taskOut{}, err
					}
					return nil, svc.view(doc), nil
				})
		}

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "transition",
			Description: "Move a task to a new state. id and to are required; this write applies dependency and check gates, and closing auto-runs command checks and refuses on failure."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in transitionIn) (*mcpsdk.CallToolResult, taskOut, error) {
				doc, err := svc.TransitionContext(ctx, in.ID, in.To)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "run_checks",
			Description: "Run and record a task's command checks (all by default, or given indices). id is required; this writes check results, while manual checks are skipped and must be attested."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in runChecksIn) (*mcpsdk.CallToolResult, taskOut, error) {
				doc, err := svc.RunChecksContext(ctx, in.ID, in.Only)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "note",
			Description: "Append a free-text provenance entry to a task. id and text are required; this durable write is attributed to the fixed connection actor."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in noteIn) (*mcpsdk.CallToolResult, taskOut, error) {
				doc, err := svc.Note(in.ID, in.Text)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "edit_note",
			Description: "Edit one task note in place and mark editedAt. id and text are required; address a stable note ID or legacy index. This durable write can edit note entries only, never system provenance."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in editNoteIn) (*mcpsdk.CallToolResult, taskOut, error) {
				idx := -1
				if in.Index != nil {
					idx = *in.Index
				}
				doc, err := svc.EditNote(in.ID, in.Note, idx, in.Text)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "delete_note",
			Description: "Delete one task note by stable note ID or legacy index. id is required; this durable write can delete note entries only, never system provenance."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in deleteNoteIn) (*mcpsdk.CallToolResult, taskOut, error) {
				idx := -1
				if in.Index != nil {
					idx = *in.Index
				}
				doc, err := svc.DeleteNote(in.ID, in.Note, idx)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "attest",
			Description: "Attest a manual check (one with no cmd). id and index are required; this write records pass by default or an explicit fail. Command checks must be run through run_checks, never attested."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in attestIn) (*mcpsdk.CallToolResult, taskOut, error) {
				doc, err := svc.Attest(in.ID, in.Index, in.Pass == nil || *in.Pass)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "begin",
			Description: fmt.Sprintf("Begin an observable work session. id, expected_actor, and idempotency_key are required; this connection writes as %s and expected_actor must match exactly. The write claims/starts work; heartbeat at least every heartbeatIntervalSeconds from the result.", svc.actor)},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in beginSessionIn) (*mcpsdk.CallToolResult, SessionView, error) {
				out, err := svc.BeginSession(ctx, BeginSessionInput{
					TaskID: in.ID, ExpectedActor: in.ExpectedActor, Client: in.Client, Model: in.Model,
					Worktree: in.Worktree, Branch: in.Branch, Head: in.Head, IdempotencyKey: in.IdempotencyKey,
				})
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "heartbeat",
			Description: "Refresh an active session with concise progress. session is required; this durable write refreshes liveness for the fixed actor's active session."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in heartbeatIn) (*mcpsdk.CallToolResult, SessionView, error) {
				out, err := svc.Heartbeat(ctx, HeartbeatInput{SessionID: in.Session, Progress: in.Progress})
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "finish",
			Description: "Finish an active session with a review summary. session and summary are required; this durable write requests review and never closes the task."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in finishSessionIn) (*mcpsdk.CallToolResult, SessionView, error) {
				out, err := svc.FinishSession(ctx, FinishSessionInput{SessionID: in.Session, Summary: in.Summary, Head: in.Head})
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "cancel",
			Description: "Cancel an active session, release its task assignment, and keep the task open. session and reason are required; this durable write is limited to the fixed actor's session."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in cancelSessionIn) (*mcpsdk.CallToolResult, SessionView, error) {
				out, err := svc.CancelSession(ctx, CancelSessionInput{SessionID: in.Session, Reason: in.Reason})
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "get_session",
			Description: "Read one durable session with current heartbeat health. session is required; in Carbon v2 include_cluster=true is an explicit same-cluster read opt-in with no side effect."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in sessionIDIn) (*mcpsdk.CallToolResult, SessionView, error) {
				out, err := svc.GetSessionScoped(in.Session, in.IncludeCluster)
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "list_sessions",
			Description: "Read observable sessions newest first, optionally filtered by task, actor, status, or health. It has no side effect; in Carbon v2 include_cluster=true is an explicit same-cluster read opt-in."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in listSessionsIn) (*mcpsdk.CallToolResult, sessionsOut, error) {
				views, err := svc.ListSessionsScoped(in.Task, in.Actor, in.Status, in.Health, in.IncludeCluster)
				return nil, sessionsOut{Sessions: views}, err
			})
	}

	// Carbon v2 stable coordination tools. They deliberately use the same scoped Service methods
	// as HTTP: a project-bound connection defaults to its own project, and only an
	// explicit include_cluster opt-in broadens reads within the current cluster.
	if carbonStableTasks {
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "worker_identity_list",
			Description: "Carbon v2 stable read: list this selected project's Worker identity records and whether Identity Mode is enabled. Reading is safe while disabled; records are retained for a later re-enable."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, WorkerIdentitySnapshot, error) {
				out, err := svc.ListWorkerIdentities()
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "worker_identity_get",
			Description: "Carbon v2 stable read: get one canonical agent Worker identity in this selected project. actor is required and no task ownership changes."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in workerIdentityGetIn) (*mcpsdk.CallToolResult, WorkerIdentityResult, error) {
				out, err := svc.GetWorkerIdentity(in.Actor)
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "worker_identity_audit_list",
			Description: "Carbon v2 stable read: list append-only Worker identity claim/change audits for this selected project. Audits remain visible even when Identity Mode enforcement is currently disabled."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, WorkerIdentityAuditSnapshot, error) {
				out, err := svc.ListWorkerIdentityAudit()
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "subscription_initialize",
			Description: "Carbon v2 stable write: create or deliberately update this Agent's selected-project event subscription for result-oriented tasks and process-oriented Incidents. passive, mixed, and active currently deliver by durable events_poll only: pushSupported is false until this MCP session has a verified push capability. Repeating the same idempotency_key and exact request is safe; changing an existing subscription requires expected_version and a new key."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in subscriptionInitializeIn) (*mcpsdk.CallToolResult, subscription.InitializeResult, error) {
				out, err := svc.InitializeEventSubscription(ctx, subscription.InitializeInput{
					SubscriptionID: in.SubscriptionID, IdempotencyKey: in.IdempotencyKey, ExpectedVersion: in.ExpectedVersion,
					Mode: subscription.Mode(in.Mode), Modules: toSubscriptionModules(in.Modules),
					Tasks:     subscription.TaskFilter{Statuses: append([]string(nil), in.TaskFilters.Statuses...), Types: append([]string(nil), in.TaskFilters.Types...), Importances: append([]string(nil), in.TaskFilters.Importances...)},
					Incidents: subscription.IncidentFilter{Statuses: append([]string(nil), in.IncidentFilters.Statuses...), Severities: append([]string(nil), in.IncidentFilters.Severities...), Kinds: append([]string(nil), in.IncidentFilters.Kinds...)},
				})
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "events_poll",
			Description: "Carbon v2 stable read: durably poll this Agent's selected-project subscription cursor. It returns safe event summaries only, never task/Incident bodies or discussion text. cursor is project/actor/subscription-bound; an expired slow cursor must be explicitly resynchronized through subscription_initialize. wait_ms is bounded to 30000 and does not claim push delivery."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in eventsPollIn) (*mcpsdk.CallToolResult, subscription.PollResult, error) {
				if in.WaitMS > 30_000 {
					return nil, subscription.PollResult{}, fmt.Errorf("events_poll wait_ms must be between 0 and 30000")
				}
				out, err := svc.PollEventSubscription(ctx, subscription.PollInput{SubscriptionID: in.SubscriptionID, Cursor: in.Cursor, Limit: in.Limit, Wait: time.Duration(in.WaitMS) * time.Millisecond})
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "worker_identity_claim",
			Description: "Carbon v2 stable write: claim or change this connection Agent's one-or-more stable role keys and allowed task types. Identity Mode must be enabled for Agent self-service. The first claim may omit reason; a material later change requires a non-empty reason."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in workerIdentityClaimIn) (*mcpsdk.CallToolResult, WorkerIdentityResult, error) {
				out, err := svc.ClaimWorkerIdentity(ctx, WorkerIdentityClaimInput{Roles: append([]string(nil), in.Roles...), Role: in.Role, Types: append([]string(nil), in.Types...), Reason: in.Reason})
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "incident_list",
			Description: "Carbon v2 stable read: list project-scoped Incidents. An Incident records process, investigation, or an odd ongoing event; a task records a required result. Reading never changes task activity or market history."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, incidentsOut, error) {
				items, err := svc.ListIncidents()
				return nil, incidentsOut{Incidents: items}, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "incident_get",
			Description: "Carbon v2 stable read: get one project-scoped Incident with its append-only discussion replies. Use this for process context, not task ownership or task completion."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in incidentIDIn) (*mcpsdk.CallToolResult, incident.Incident, error) {
				out, err := svc.GetIncident(in.ID)
				return nil, out, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "incident_create",
			Description: "Carbon v2 stable write: record a project-scoped process Incident (sudden, long_running, investigation, other, or a valid custom kind). This is intentionally separate from a result-oriented task and does not create task provenance."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in incidentCreateIn) (*mcpsdk.CallToolResult, incident.Incident, error) {
				out, err := svc.CreateIncident(ctx, incident.CreateInput{Kind: incident.Kind(in.Kind), RelatedTaskIDs: append([]string(nil), in.RelatedTaskIDs...), Title: in.Title, Body: in.Body, Severity: incident.Severity(in.Severity)})
				return nil, out, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "incident_reply",
			Description: "Carbon v2 stable write: append a discussion reply to an Incident. The same Agent may ask and later answer its own investigation; replies do not count as task progress."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in incidentReplyIn) (*mcpsdk.CallToolResult, incident.Reply, error) {
				out, err := svc.ReplyIncident(ctx, in.ID, in.Body)
				return nil, out, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "incident_update",
			Description: "Carbon v2 stable write: update only an Incident lifecycle (open, investigating, resolved, closed). It does not alter any linked task state, lease, or activity history."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in incidentUpdateIn) (*mcpsdk.CallToolResult, incident.Incident, error) {
				out, err := svc.UpdateIncidentLifecycle(ctx, in.ID, incident.UpdateInput{Status: incident.Status(in.Status)})
				return nil, out, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "review_list",
			Description: "Carbon v2 stable read: list explicit project review targets. These plan/manual-check reviews are independent from task lease approval and pending claims."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, reviewsOut, error) {
				items, err := svc.ListReviewTargets()
				return nil, reviewsOut{Reviews: items}, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "review_get",
			Description: "Carbon v2 stable read: get one explicit plan or manual-check review target."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in reviewIDIn) (*mcpsdk.CallToolResult, review.Target, error) {
				out, err := svc.GetReviewTarget(in.ID)
				return nil, out, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "review_create",
			Description: "Carbon v2 stable write: create an explicit review target and assign one reviewer. When Identity Mode is enabled, an Agent reviewer must hold the reviewer role; this never uses lease approval."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewCreateIn) (*mcpsdk.CallToolResult, review.Target, error) {
				out, err := svc.CreateReviewTarget(ctx, review.CreateInput{TargetKind: review.TargetKind(in.TargetKind), TargetID: in.TargetID, TaskID: in.TaskID, CheckID: in.CheckID, ReviewerActor: in.ReviewerActor})
				return nil, out, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "review_decide",
			Description: "Carbon v2 stable write: the assigned reviewer decides approved or rejected with a non-empty decision. Human/system control-plane actors may explicitly manage an assignment; ordinary Agents cannot decide a peer's target."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewDecideIn) (*mcpsdk.CallToolResult, review.Target, error) {
				out, err := svc.DecideReviewTarget(ctx, in.ID, review.DecideInput{Status: review.Status(in.Status), Decision: in.Decision})
				return nil, out, err
			})

		registerWorkLogToolsWithAccessor(srv, func() *Service { return svc })

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "set_blocker",
			Description: "Carbon v2 stable write: set or clear a task blocker reason. id, blocker_reason, and current expected_version are required; the durable reason remains after a state change until explicitly cleared."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in setBlockerIn) (*mcpsdk.CallToolResult, taskOut, error) {
				if strings.TrimSpace(in.ExpectedVersion) == "" {
					return nil, taskOut{}, errEvidenceExpectedVersionRequired
				}
				doc, err := svc.SetBlockerWithVersion(in.ID, in.BlockerReason, in.ExpectedVersion)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "add_evidence",
			Description: "Carbon v2 stable write: add one durable evidence item. id, kind, value, and current expected_version are required; the server assigns its ID and creation audit fields."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in addEvidenceIn) (*mcpsdk.CallToolResult, taskOut, error) {
				if strings.TrimSpace(in.ExpectedVersion) == "" {
					return nil, taskOut{}, errEvidenceExpectedVersionRequired
				}
				doc, err := svc.AddEvidenceWithVersion(in.ID, task.Evidence{Kind: in.Kind, Value: in.Value, Label: in.Label, URL: in.URL}, in.ExpectedVersion)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "remove_evidence",
			Description: "Carbon v2 stable write: remove one task evidence item by server-assigned evidence_id. id, evidence_id, and current expected_version are required."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in removeEvidenceIn) (*mcpsdk.CallToolResult, taskOut, error) {
				if strings.TrimSpace(in.ExpectedVersion) == "" {
					return nil, taskOut{}, errEvidenceExpectedVersionRequired
				}
				doc, err := svc.RemoveEvidenceWithVersion(in.ID, in.EvidenceID, in.ExpectedVersion)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "search",
			Description: "Carbon v2 stable read: search tasks by text and metadata. A project-bound connection searches only its project by default; include_cluster=true is an explicit same-cluster read opt-in and never changes write scope."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in searchIn) (*mcpsdk.CallToolResult, searchOut, error) {
				results, err := svc.Search(search.Query{Text: in.Text, ProjectID: in.ProjectID, Type: in.Type, Importance: in.Importance, Status: in.Status, Assignee: in.Assignee, Labels: in.Labels}, in.IncludeCluster)
				if err != nil {
					return nil, searchOut{}, err
				}
				return nil, searchResultsOut(svc, results), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "lease_claim",
			Description: "Carbon v2 stable write: claim a durable execution lease. id, non-empty reason, and current expected_version are required; a conflict creates a pending approval request (pending=true) instead of overwriting ownership."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in leaseClaimIn) (*mcpsdk.CallToolResult, leaseClaimOut, error) {
				if strings.TrimSpace(in.Reason) == "" {
					return nil, leaseClaimOut{}, lease.ErrReasonRequired
				}
				if strings.TrimSpace(in.ExpectedVersion) == "" {
					return nil, leaseClaimOut{}, errLeaseClaimExpectedVersionRequired
				}
				result, err := svc.ClaimLease(ctx, LeaseClaimInput{TaskID: in.ID, TTL: secondsDuration(in.TTLSeconds), RequestID: in.RequestID, Reason: in.Reason, ExpectedVersion: in.ExpectedVersion})
				if err != nil && !errors.Is(err, lease.ErrApprovalPending) {
					return nil, leaseClaimOut{}, err
				}
				out := leaseClaimOut{Pending: result.Pending, Request: result.Request}
				if result.Doc != nil {
					out.Task = svc.view(result.Doc)
				}
				return nil, out, nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "lease_status",
			Description: "Carbon v2 stable read: get a task's active lease and pending approval requests. id is required; include_cluster=true is required to inspect another project in this same cluster, and the read has no side effect."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in leaseStatusIn) (*mcpsdk.CallToolResult, leaseStatusOut, error) {
				doc, err := svc.GetScoped(in.ID, in.IncludeCluster)
				if err != nil {
					return nil, leaseStatusOut{}, err
				}
				return nil, leaseStatusOut{ID: doc.Task.ID, Lease: doc.Task.Lease, PendingClaims: doc.Task.PendingClaims, Version: doc.Version()}, nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "lease_renew",
			Description: "Carbon v2 stable write: renew this connection actor's lease. id and lease_id are required; expected_version is optional optimistic concurrency and a supplied stale value is rejected."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in leaseRenewIn) (*mcpsdk.CallToolResult, taskOut, error) {
				doc, err := svc.RenewLease(ctx, in.ID, in.LeaseID, secondsDuration(in.TTLSeconds), in.ExpectedVersion)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "lease_release",
			Description: "Carbon v2 stable write: release this connection actor's lease. id and lease_id are required; keep_assignee=true retains visible attribution, and an optional expected_version rejects stale writes."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in leaseReleaseIn) (*mcpsdk.CallToolResult, taskOut, error) {
				doc, err := svc.ReleaseLease(ctx, in.ID, in.LeaseID, in.Reason, in.ExpectedVersion, in.KeepAssignee)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "lease_reassign",
			Description: "Carbon v2 stable write: reassign a task lease. id and assignee are required; replacing an existing holder requires force=true and a reason, while an optional expected_version provides optimistic concurrency."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in leaseReassignIn) (*mcpsdk.CallToolResult, taskOut, error) {
				doc, err := svc.ReassignLease(ctx, in.ID, in.Assignee, in.Reason, in.ExpectedVersion, in.Force)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "lease_approve",
			Description: "Carbon v2 stable write: approve or reject one pending lease claim. id, request_id, approve, and a non-empty reason are required; optional expected_version rejects a stale task revision."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in leaseApproveIn) (*mcpsdk.CallToolResult, taskOut, error) {
				doc, err := svc.ApproveLeaseClaim(ctx, in.ID, in.RequestID, in.Reason, in.ExpectedVersion, in.Approve)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "trash",
			Description: "Carbon v2 stable write: move one task to recoverable trash. id is required; expected_version is optional optimistic concurrency. This MCP surface never permanently empties trash."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in trashOneIn) (*mcpsdk.CallToolResult, trashOut, error) {
				entry, err := svc.TrashTask(ctx, in.ID, in.Reason, in.ExpectedVersion)
				return nil, trashOut{Entries: []trash.Entry{entry}}, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "trash_many",
			Description: "Carbon v2 stable write: atomically move 1-100 tasks to recoverable trash. ids are required and expected_versions is keyed by task ID; every selected task needs a current version."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in trashManyIn) (*mcpsdk.CallToolResult, trashOut, error) {
				entries, err := svc.TrashTasks(ctx, in.IDs, in.Reason, NormalizeExpectedVersions(in.ExpectedVersions))
				return nil, trashOut{Entries: entries}, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "list_trash",
			Description: "Carbon v2 stable read: list recoverable trash entries. include_cluster=true explicitly includes other projects in this same cluster; it has no side effect."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in includeClusterIn) (*mcpsdk.CallToolResult, trashOut, error) {
				entries, err := svc.ListTrash(in.IncludeCluster)
				return nil, trashOut{Entries: entries}, err
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "restore_trash",
			Description: "Carbon v2 stable write: restore one trash entry. id is required; project_id is optional, and an explicit empty value restores cluster-wide. expected_version is optional optimistic concurrency."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in restoreTrashIn) (*mcpsdk.CallToolResult, taskOut, error) {
				doc, err := svc.RestoreTrash(ctx, in.ID, in.ProjectID, in.ExpectedVersion)
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "bulk_update",
			Description: "Carbon v2 stable write: atomically edit 1-100 tasks. ids and a current expected_versions entry for every ID are required; all fields validate before any write. assignee is legacy-only, so Carbon uses lease_reassign."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in bulkUpdateIn) (*mcpsdk.CallToolResult, taskListOut, error) {
				docs, err := svc.BulkUpdate(ctx, store.BulkUpdate{IDs: in.IDs, ExpectedVersions: NormalizeExpectedVersions(in.ExpectedVersions), ProjectID: in.ProjectID, Type: in.Type, Importance: in.Importance, Priority: in.Priority, Labels: in.Labels, Assignee: in.Assignee, Parent: in.Parent, Status: in.Status, Force: in.Force, Reason: in.Reason})
				if err != nil {
					return nil, taskListOut{}, err
				}
				return nil, taskDocsOut(svc, docs), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "bulk_move",
			Description: "Carbon v2 stable write: atomically move 1-100 tasks within this cluster. ids and current expected_versions are required; project_id must be a same-cluster project or empty with cluster_wide=true. A project-bound cross-project move requires force=true and a reason."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in bulkMoveIn) (*mcpsdk.CallToolResult, taskListOut, error) {
				docs, err := svc.BulkMoveWithAuthorization(ctx, store.BulkMove{IDs: in.IDs, ExpectedVersions: NormalizeExpectedVersions(in.ExpectedVersions), ProjectID: in.ProjectID, ClusterWide: in.ClusterWide, Parent: in.Parent, Reason: in.Reason}, in.Force)
				if err != nil {
					return nil, taskListOut{}, err
				}
				return nil, taskDocsOut(svc, docs), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "list_views",
			Description: "Carbon v2 stable read: list saved task-search views in this physical cluster. It has no side effect."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, viewsOut, error) {
				list, err := svc.ListViews()
				return nil, viewsOut{Views: list}, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "get_view",
			Description: "Carbon v2 stable read: get one saved view and its version token. id is required; use that token as expected_version for a protected update or delete, with no side effect."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in namedIDIn) (*mcpsdk.CallToolResult, viewOut, error) {
				view, err := svc.GetView(in.ID)
				return nil, viewOut{View: view}, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "create_view",
			Description: "Carbon v2 stable write: create a saved search view. name and query are required; query project_id is scoped to the connection unless include_cluster=true explicitly permits a same-cluster read query."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in viewWriteIn) (*mcpsdk.CallToolResult, viewOut, error) {
				view, err := svc.CreateView(ctx, views.View{ID: in.ID, Name: in.Name, Query: in.Query}, in.IncludeCluster)
				return nil, viewOut{View: view}, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "save_view",
			Description: "Carbon v2 stable write: save a named view. id, name, and query are required; expected_version is optional optimistic concurrency and a supplied stale token is rejected."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in viewWriteIn) (*mcpsdk.CallToolResult, viewOut, error) {
				view, err := svc.SaveView(ctx, views.View{ID: in.ID, Name: in.Name, Query: in.Query}, in.ExpectedVersion, in.IncludeCluster)
				return nil, viewOut{View: view}, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "delete_view",
			Description: "Carbon v2 stable write: delete one saved view. id is required; expected_version is optional optimistic concurrency and a supplied stale token is rejected."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in deleteNamedIn) (*mcpsdk.CallToolResult, deletedOut, error) {
				err := svc.DeleteView(ctx, in.ID, in.ExpectedVersion)
				return nil, deletedOut{ID: in.ID, Deleted: err == nil}, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "apply_view",
			Description: "Carbon v2 stable read: apply a saved view. id is required; include_cluster=true is an explicit same-cluster read opt-in and never expands write scope."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in applyViewIn) (*mcpsdk.CallToolResult, searchOut, error) {
				results, err := svc.ApplyView(in.ID, in.IncludeCluster)
				if err != nil {
					return nil, searchOut{}, err
				}
				return nil, searchResultsOut(svc, results), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "list_templates",
			Description: "Carbon v2 stable read: list reusable explicit task templates in this cluster. It has no side effect."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, templatesOut, error) {
				list, err := svc.ListTemplates()
				return nil, templatesOut{Templates: list}, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "get_template",
			Description: "Carbon v2 stable read: get one task template and its version token. id is required; use that token as expected_version for a protected write or instantiation, with no side effect."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in namedIDIn) (*mcpsdk.CallToolResult, templateOut, error) {
				template, err := svc.GetTemplate(in.ID)
				return nil, templateOut{Template: template}, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "create_template",
			Description: "Carbon v2 stable write: create an explicit task template. name and title are required; templates must name project_id or set cluster_wide=true and must specify type and importance."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in templateWriteIn) (*mcpsdk.CallToolResult, templateOut, error) {
				template, err := svc.CreateTemplate(ctx, templateFromTool(in))
				return nil, templateOut{Template: template}, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "save_template",
			Description: "Carbon v2 stable write: save a template with project_id/type/importance. id, name, and title are required; expected_version is optional optimistic concurrency and a supplied stale token is rejected."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in templateWriteIn) (*mcpsdk.CallToolResult, templateOut, error) {
				template, err := svc.SaveTemplate(ctx, templateFromTool(in), in.ExpectedVersion)
				return nil, templateOut{Template: template}, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "delete_template",
			Description: "Carbon v2 stable write: delete one template. id is required; expected_version is optional optimistic concurrency and a supplied stale token is rejected."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in deleteNamedIn) (*mcpsdk.CallToolResult, deletedOut, error) {
				err := svc.DeleteTemplate(ctx, in.ID, in.ExpectedVersion)
				return nil, deletedOut{ID: in.ID, Deleted: err == nil}, err
			})
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "instantiate_template",
			Description: "Carbon v2 stable write: instantiate a template as a task. id is required; explicit overrides preserve project_id/type/importance semantics, and expected_version protects the template revision when supplied."},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in instantiateTemplateIn) (*mcpsdk.CallToolResult, taskOut, error) {
				doc, err := svc.InstantiateTemplate(ctx, templates.InstantiateInput{TemplateID: in.ID, ExpectedVersion: in.ExpectedVersion, Title: in.Title, Body: in.Body, ProjectID: in.ProjectID, Type: in.Type, Importance: in.Importance, Priority: in.Priority, Labels: in.Labels, Deps: in.Deps, Parent: in.Parent, Checks: checksPointer(in.Checks)})
				if err != nil {
					return nil, taskOut{}, err
				}
				return nil, svc.view(doc), nil
			})

		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "worker_stats",
			Description: "Carbon v2 stable read: compute assignment, completion, reopen, and cycle metrics. A project-bound connection reports only its project unless include_cluster=true explicitly reads the same cluster; it has no side effect."},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, in includeClusterIn) (*mcpsdk.CallToolResult, stats.Report, error) {
				report, err := svc.WorkerStats(in.IncludeCluster)
				return nil, report, err
			})
	}

	return srv
}

// --- tool I/O schemas (jsonschema tags drive the MCP input schema) ---

type catalogReferenceIn struct {
	Reference string `json:"reference" jsonschema:"stable cluster id, machine slug/alias, or unique display name"`
}

type catalogProjectReferenceIn struct {
	Cluster string `json:"cluster,omitempty" jsonschema:"optional shared-cluster id, machine slug/alias, or unique display name; omit for a global fail-closed standalone-or-cluster lookup"`
	Project string `json:"project" jsonschema:"stable project id, machine slug/alias, or unique display name"`
}

type listCatalogProjectsIn struct {
	Cluster string `json:"cluster,omitempty" jsonschema:"optional cluster id, slug/alias, or unique display name; omit to list all projects in the bound Carbon home"`
}

type createCatalogClusterIn struct {
	Name        string   `json:"name" jsonschema:"human-facing cluster name"`
	Slug        string   `json:"slug,omitempty" jsonschema:"optional lowercase machine-safe English slug; generated from name when omitted"`
	Description string   `json:"description,omitempty" jsonschema:"optional cluster description"`
	SlugAliases []string `json:"slug_aliases,omitempty" jsonschema:"optional historical lowercase machine-safe slug aliases"`
	Prefix      string   `json:"prefix,omitempty" jsonschema:"optional task id prefix"`
	AllowCreate bool     `json:"allow_create" jsonschema:"must be true; creation is never inferred from a missing lookup"`
	Reason      string   `json:"reason" jsonschema:"non-empty reason why this durable cluster must be created"`
}

type createCatalogProjectIn struct {
	Cluster     string           `json:"cluster,omitempty" jsonschema:"optional target shared cluster id, slug/alias, or unique display name; omit to create an isolated standalone project"`
	Name        string           `json:"name" jsonschema:"human-facing project name"`
	Slug        string           `json:"slug,omitempty" jsonschema:"optional lowercase machine-safe English slug; generated from name when omitted"`
	Description string           `json:"description,omitempty" jsonschema:"optional project description"`
	SlugAliases []string         `json:"slug_aliases,omitempty" jsonschema:"optional historical lowercase machine-safe slug aliases"`
	Kind        home.ProjectKind `json:"kind,omitempty" jsonschema:"project surface kind such as generic, pc, mobile, ios, web, backend, library, service, or other"`
	SourcePath  string           `json:"source_path" jsonschema:"required existing local source directory for this project"`
	AllowCreate bool             `json:"allow_create" jsonschema:"must be true; creation is never inferred from a missing lookup"`
	Reason      string           `json:"reason" jsonschema:"non-empty reason why this durable project must be created"`
}

type selectProjectIn struct {
	Cluster string `json:"cluster,omitempty" jsonschema:"optional shared-cluster id, slug/alias, or unique display name; omit only when selecting a standalone project or an unambiguous project across this Home"`
	Project string `json:"project" jsonschema:"required stable project id, machine slug/alias, or unique display name to make active for this Project Session"`
}

type clusterOut struct {
	Cluster ClusterCatalog `json:"cluster"`
}

type clustersOut struct {
	Clusters []ClusterCatalog `json:"clusters"`
}

type projectsOut struct {
	Projects []ProjectCatalog `json:"projects"`
}

type idIn struct {
	ID string `json:"id" jsonschema:"the task id, e.g. PROJ-01j8x2k7q7f3az"`
}

type getIn struct {
	ID             string `json:"id" jsonschema:"the task id, e.g. PROJ-01j8x2k7q7f3az"`
	IncludeCluster bool   `json:"include_cluster,omitempty" jsonschema:"explicitly allow a read from another project in this same Carbon cluster; never crosses clusters"`
}

type listIn struct {
	Status         string `json:"status,omitempty" jsonschema:"filter by status"`
	Assignee       string `json:"assignee,omitempty" jsonschema:"filter by assignee, e.g. agent:claude-1"`
	Ready          *bool  `json:"ready,omitempty" jsonschema:"filter by derived deps-satisfied readiness"`
	Execution      string `json:"execution,omitempty" jsonschema:"filter by derived execution state: active, stalled, or awaiting_review"`
	IncludeCluster bool   `json:"include_cluster,omitempty" jsonschema:"explicitly list all projects in this same Carbon cluster; default is the bound project only"`
}

type beginSessionIn struct {
	ID             string `json:"id" jsonschema:"task id"`
	ExpectedActor  string `json:"expected_actor" jsonschema:"actor the caller expects this connection to use; must match identity exactly"`
	Client         string `json:"client,omitempty" jsonschema:"agent client, e.g. codex or claude"`
	Model          string `json:"model,omitempty" jsonschema:"model identifier when known"`
	Worktree       string `json:"worktree,omitempty" jsonschema:"local worktree path"`
	Branch         string `json:"branch,omitempty" jsonschema:"current Git branch"`
	Head           string `json:"head,omitempty" jsonschema:"current Git HEAD"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"unique retry key for this begin operation"`
}

type heartbeatIn struct {
	Session  string `json:"session" jsonschema:"session id"`
	Progress string `json:"progress,omitempty" jsonschema:"concise current progress, not chain-of-thought"`
}

type finishSessionIn struct {
	Session string `json:"session" jsonschema:"session id"`
	Summary string `json:"summary" jsonschema:"review-ready summary of the completed attempt"`
	Head    string `json:"head,omitempty" jsonschema:"ending Git HEAD"`
}

type cancelSessionIn struct {
	Session string `json:"session" jsonschema:"session id"`
	Reason  string `json:"reason" jsonschema:"why the attempt was abandoned"`
}

type sessionIDIn struct {
	Session        string `json:"session" jsonschema:"session id"`
	IncludeCluster bool   `json:"include_cluster,omitempty" jsonschema:"explicit same-cluster read opt-in"`
}

type listSessionsIn struct {
	Task           string `json:"task,omitempty" jsonschema:"filter by task id"`
	Actor          string `json:"actor,omitempty" jsonschema:"filter by actor"`
	Status         string `json:"status,omitempty" jsonschema:"filter by durable status"`
	Health         string `json:"health,omitempty" jsonschema:"filter by derived health"`
	IncludeCluster bool   `json:"include_cluster,omitempty" jsonschema:"explicit same-cluster read opt-in"`
}

type sessionsOut struct {
	Sessions []SessionView `json:"sessions"`
}

type workerIdentityGetIn struct {
	Actor string `json:"actor" jsonschema:"required canonical agent actor, for example agent:architect"`
}

type workerIdentityClaimIn struct {
	Roles  []string `json:"roles,omitempty" jsonschema:"one or more stable machine role keys, for example [frontend,backend] or [reviewer]"`
	Role   string   `json:"role,omitempty" jsonschema:"legacy primary-role alias; use roles for new callers"`
	Types  []string `json:"types" jsonschema:"required one or more current task type keys this Worker may claim"`
	Reason string   `json:"reason,omitempty" jsonschema:"required when changing an existing role or type assignment"`
}

type subscriptionTaskFiltersIn struct {
	Statuses    []string `json:"statuses,omitempty" jsonschema:"optional task status allow-list; empty means all"`
	Types       []string `json:"types,omitempty" jsonschema:"optional task type allow-list; empty means all"`
	Importances []string `json:"importances,omitempty" jsonschema:"optional task importance allow-list; empty means all"`
}

type subscriptionIncidentFiltersIn struct {
	Statuses   []string `json:"statuses,omitempty" jsonschema:"optional Incident lifecycle allow-list; empty means all"`
	Severities []string `json:"severities,omitempty" jsonschema:"optional Incident severity allow-list; empty means all"`
	Kinds      []string `json:"kinds,omitempty" jsonschema:"optional Incident kind allow-list; empty means all"`
}

type subscriptionInitializeIn struct {
	SubscriptionID  string                        `json:"subscription_id" jsonschema:"stable caller-chosen subscription id"`
	IdempotencyKey  string                        `json:"idempotency_key" jsonschema:"stable request key; same key must carry the identical request"`
	ExpectedVersion *uint64                       `json:"expected_version,omitempty" jsonschema:"current subscription version, required only when changing an existing subscription"`
	Mode            string                        `json:"mode" jsonschema:"passive|mixed|active; all currently use durable polling"`
	Modules         []string                      `json:"modules" jsonschema:"one or both of tasks,incidents"`
	TaskFilters     subscriptionTaskFiltersIn     `json:"task_filters,omitempty" jsonschema:"task-only filters"`
	IncidentFilters subscriptionIncidentFiltersIn `json:"incident_filters,omitempty" jsonschema:"Incident-only filters"`
}

type eventsPollIn struct {
	SubscriptionID string `json:"subscription_id" jsonschema:"stable subscription id"`
	Cursor         string `json:"cursor,omitempty" jsonschema:"previous cursor returned by subscription_initialize or events_poll"`
	Limit          int    `json:"limit,omitempty" jsonschema:"1-200 safe events; defaults to 50"`
	WaitMS         int    `json:"wait_ms,omitempty" jsonschema:"0-30000 long-poll milliseconds; no push delivery is implied"`
}

func toSubscriptionModules(items []string) []subscription.Module {
	out := make([]subscription.Module, len(items))
	for index, item := range items {
		out[index] = subscription.Module(item)
	}
	return out
}

type incidentIDIn struct {
	ID string `json:"id" jsonschema:"project-scoped incident id"`
}

type incidentCreateIn struct {
	Kind           string   `json:"kind,omitempty" jsonschema:"built-in sudden|long_running|investigation|other or a valid future custom machine key"`
	RelatedTaskIDs []string `json:"related_task_ids,omitempty" jsonschema:"optional existing task ids in the selected project; this links context only and never creates task progress"`
	Title          string   `json:"title" jsonschema:"required concise process/event title"`
	Body           string   `json:"body,omitempty" jsonschema:"optional investigation context"`
	Severity       string   `json:"severity,omitempty" jsonschema:"info|low|normal|high|urgent; defaults to normal"`
}

type incidentReplyIn struct {
	ID   string `json:"id" jsonschema:"incident id"`
	Body string `json:"body" jsonschema:"required append-only discussion reply"`
}

type incidentUpdateIn struct {
	ID     string `json:"id" jsonschema:"incident id"`
	Status string `json:"status" jsonschema:"open|investigating|resolved|closed"`
}

type incidentsOut struct {
	Incidents []incident.Incident `json:"incidents"`
}

type reviewIDIn struct {
	ID string `json:"id" jsonschema:"project-scoped review target id"`
}

type reviewCreateIn struct {
	TargetKind    string `json:"target_kind" jsonschema:"plan|manual_check"`
	TargetID      string `json:"target_id" jsonschema:"required target identifier"`
	TaskID        string `json:"task_id,omitempty" jsonschema:"optional existing task id in the selected project"`
	CheckID       string `json:"check_id,omitempty" jsonschema:"optional check metadata id"`
	ReviewerActor string `json:"reviewer_actor" jsonschema:"required explicitly assigned agent/human/system reviewer"`
}

type reviewDecideIn struct {
	ID       string `json:"id" jsonschema:"review target id"`
	Status   string `json:"status" jsonschema:"approved|rejected"`
	Decision string `json:"decision" jsonschema:"required concise reviewer decision"`
}

type reviewsOut struct {
	Reviews []review.Target `json:"reviews"`
}

type includeClusterIn struct {
	IncludeCluster bool `json:"include_cluster,omitempty" jsonschema:"explicitly allow a read across projects in this same Carbon cluster"`
}

type searchIn struct {
	Text           string   `json:"text,omitempty" jsonschema:"full-text query"`
	ProjectID      *string  `json:"project_id,omitempty" jsonschema:"same-cluster project id filter; project-bound connections override this unless include_cluster=true"`
	Type           string   `json:"type,omitempty" jsonschema:"task type filter"`
	Importance     string   `json:"importance,omitempty" jsonschema:"task importance filter"`
	Status         string   `json:"status,omitempty" jsonschema:"task status filter"`
	Assignee       string   `json:"assignee,omitempty" jsonschema:"assignee filter"`
	Labels         []string `json:"labels,omitempty" jsonschema:"all labels that must be present"`
	IncludeCluster bool     `json:"include_cluster,omitempty" jsonschema:"explicit same-cluster read opt-in"`
}

type searchResultOut struct {
	ClusterID  string             `json:"cluster_id,omitempty"`
	Task       taskOut            `json:"task"`
	Score      int                `json:"score"`
	Highlights []search.Highlight `json:"highlights,omitempty"`
}

type searchOut struct {
	Results []searchResultOut `json:"results"`
}

type leaseClaimIn struct {
	ID              string `json:"id" jsonschema:"task id"`
	TTLSeconds      int    `json:"ttl_seconds,omitempty" jsonschema:"lease duration in seconds; default is 900"`
	RequestID       string `json:"request_id,omitempty" jsonschema:"retry a returned pending request idempotently"`
	Reason          string `json:"reason" jsonschema:"required non-empty claim context"`
	ExpectedVersion string `json:"expected_version" jsonschema:"required raw version or quoted ETag optimistic-concurrency token"`
}

type leaseClaimOut struct {
	Task    taskOut            `json:"task"`
	Pending bool               `json:"pending"`
	Request *task.ClaimRequest `json:"request,omitempty"`
}

type leaseStatusIn struct {
	ID             string `json:"id" jsonschema:"task id"`
	IncludeCluster bool   `json:"include_cluster,omitempty" jsonschema:"explicit same-cluster read opt-in"`
}

type leaseStatusOut struct {
	ID            string              `json:"id"`
	Lease         *task.Lease         `json:"lease,omitempty"`
	PendingClaims []task.ClaimRequest `json:"pending_claims,omitempty"`
	Version       string              `json:"version,omitempty"`
}

type leaseRenewIn struct {
	ID              string `json:"id" jsonschema:"task id"`
	LeaseID         string `json:"lease_id" jsonschema:"active lease id"`
	TTLSeconds      int    `json:"ttl_seconds,omitempty" jsonschema:"new lease duration in seconds"`
	ExpectedVersion string `json:"expected_version,omitempty" jsonschema:"optional raw version or quoted ETag"`
}

type leaseReleaseIn struct {
	ID              string `json:"id" jsonschema:"task id"`
	LeaseID         string `json:"lease_id" jsonschema:"active lease id"`
	Reason          string `json:"reason,omitempty" jsonschema:"release context"`
	KeepAssignee    bool   `json:"keep_assignee,omitempty" jsonschema:"retain visible assignee after release"`
	ExpectedVersion string `json:"expected_version,omitempty" jsonschema:"optional raw version or quoted ETag"`
}

type leaseReassignIn struct {
	ID              string `json:"id" jsonschema:"task id"`
	Assignee        string `json:"assignee" jsonschema:"new assignee; empty clears"`
	Force           bool   `json:"force,omitempty" jsonschema:"required to replace an active assignee or lease holder"`
	Reason          string `json:"reason,omitempty" jsonschema:"required audit reason when force is true"`
	ExpectedVersion string `json:"expected_version,omitempty" jsonschema:"optional raw version or quoted ETag"`
}

type leaseApproveIn struct {
	ID              string `json:"id" jsonschema:"task id"`
	RequestID       string `json:"request_id" jsonschema:"pending claim request id"`
	Approve         bool   `json:"approve" jsonschema:"true grants the lease; false rejects the request"`
	Reason          string `json:"reason" jsonschema:"required decision reason"`
	ExpectedVersion string `json:"expected_version,omitempty" jsonschema:"optional raw version or quoted ETag"`
}

type trashOneIn struct {
	ID              string `json:"id" jsonschema:"task id"`
	Reason          string `json:"reason,omitempty" jsonschema:"why this task is being trashed"`
	ExpectedVersion string `json:"expected_version,omitempty" jsonschema:"optional raw version or quoted ETag"`
}

type trashManyIn struct {
	IDs              []string          `json:"ids" jsonschema:"1-100 task ids"`
	Reason           string            `json:"reason,omitempty" jsonschema:"why these tasks are being trashed"`
	ExpectedVersions map[string]string `json:"expected_versions,omitempty" jsonschema:"optional version or ETag keyed by task id"`
}

type trashOut struct {
	Entries []trash.Entry `json:"entries"`
}

type restoreTrashIn struct {
	ID              string  `json:"id" jsonschema:"trashed task id"`
	ProjectID       *string `json:"project_id,omitempty" jsonschema:"target same-cluster project; explicit empty means cluster-wide"`
	ExpectedVersion string  `json:"expected_version,omitempty" jsonschema:"optional raw version or quoted ETag"`
}

type bulkUpdateIn struct {
	IDs              []string          `json:"ids" jsonschema:"1-100 task ids"`
	ExpectedVersions map[string]string `json:"expected_versions,omitempty" jsonschema:"optional versions or ETags keyed by task id"`
	ProjectID        *string           `json:"project_id,omitempty" jsonschema:"same-cluster target project; explicit empty makes tasks cluster-wide"`
	Type             *string           `json:"type,omitempty" jsonschema:"configured task type"`
	Importance       *string           `json:"importance,omitempty" jsonschema:"core, important, normal, optional, or experimental"`
	Priority         *string           `json:"priority,omitempty" jsonschema:"low, medium, high, urgent, or empty to clear"`
	Labels           *[]string         `json:"labels,omitempty" jsonschema:"replacement label list"`
	Assignee         *string           `json:"assignee,omitempty" jsonschema:"legacy mode only; Carbon rejects direct assignment and requires lease_reassign"`
	Parent           *string           `json:"parent,omitempty" jsonschema:"parent task id"`
	Status           *string           `json:"status,omitempty" jsonschema:"target status; gates are still enforced"`
	Force            bool              `json:"force,omitempty" jsonschema:"required when replacing assignment/lease"`
	Reason           string            `json:"reason,omitempty" jsonschema:"required audit reason for forced assignment changes"`
}

type bulkMoveIn struct {
	IDs              []string          `json:"ids" jsonschema:"1-100 task ids"`
	ExpectedVersions map[string]string `json:"expected_versions,omitempty" jsonschema:"optional versions or ETags keyed by task id"`
	ProjectID        string            `json:"project_id" jsonschema:"same-cluster target project id; empty only with cluster_wide=true"`
	ClusterWide      bool              `json:"cluster_wide,omitempty" jsonschema:"explicitly move tasks to cluster-wide scope"`
	Parent           *string           `json:"parent,omitempty" jsonschema:"optional replacement parent"`
	Force            bool              `json:"force,omitempty" jsonschema:"required with a reason for project-bound cross-project moves"`
	Reason           string            `json:"reason,omitempty" jsonschema:"move reason"`
}

type taskListOut struct {
	Tasks []taskOut `json:"tasks"`
}

type namedIDIn struct {
	ID string `json:"id" jsonschema:"stable saved view or template id"`
}

type deleteNamedIn struct {
	ID              string `json:"id" jsonschema:"stable saved view or template id"`
	ExpectedVersion string `json:"expected_version,omitempty" jsonschema:"optional raw version or quoted ETag"`
}

type deletedOut struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

type viewWriteIn struct {
	ID              string       `json:"id,omitempty" jsonschema:"view id; omit on create"`
	Name            string       `json:"name" jsonschema:"human-readable view name"`
	Query           search.Query `json:"query" jsonschema:"saved search query; use query.project_id for an explicit same-cluster filter"`
	IncludeCluster  bool         `json:"include_cluster,omitempty" jsonschema:"explicit same-cluster scope opt-in"`
	ExpectedVersion string       `json:"expected_version,omitempty" jsonschema:"optional raw version or quoted ETag for save"`
}

type applyViewIn struct {
	ID             string `json:"id" jsonschema:"saved view id"`
	IncludeCluster bool   `json:"include_cluster,omitempty" jsonschema:"explicit same-cluster read opt-in"`
}

type viewOut struct {
	View views.View `json:"view"`
}

type viewsOut struct {
	Views []views.View `json:"views"`
}

type templateWriteIn struct {
	ID              string    `json:"id,omitempty" jsonschema:"template id; omit on create"`
	Name            string    `json:"name" jsonschema:"template name"`
	Title           string    `json:"title" jsonschema:"default task title"`
	Body            string    `json:"body,omitempty" jsonschema:"default markdown body"`
	ProjectID       string    `json:"project_id,omitempty" jsonschema:"same-cluster project id; empty only with cluster_wide=true"`
	ClusterWide     bool      `json:"cluster_wide,omitempty" jsonschema:"explicitly make this template cluster-wide"`
	Type            string    `json:"type" jsonschema:"explicit configured task type"`
	Importance      string    `json:"importance" jsonschema:"explicit task importance"`
	Priority        string    `json:"priority,omitempty" jsonschema:"default priority"`
	Labels          []string  `json:"labels,omitempty" jsonschema:"default labels"`
	Deps            []string  `json:"deps,omitempty" jsonschema:"default dependencies"`
	Checks          []checkIn `json:"checks,omitempty" jsonschema:"default gate checks"`
	Parent          string    `json:"parent,omitempty" jsonschema:"default parent task id"`
	ExpectedVersion string    `json:"expected_version,omitempty" jsonschema:"optional raw version or quoted ETag for save"`
}

type templateOut struct {
	Template templates.Template `json:"template"`
}

type templatesOut struct {
	Templates []templates.Template `json:"templates"`
}

type instantiateTemplateIn struct {
	ID              string     `json:"id" jsonschema:"template id"`
	ExpectedVersion string     `json:"expected_version,omitempty" jsonschema:"optional template version or quoted ETag"`
	Title           *string    `json:"title,omitempty" jsonschema:"override title"`
	Body            *string    `json:"body,omitempty" jsonschema:"override body"`
	ProjectID       *string    `json:"project_id,omitempty" jsonschema:"override same-cluster project; explicit empty is cluster-wide"`
	Type            *string    `json:"type,omitempty" jsonschema:"override task type"`
	Importance      *string    `json:"importance,omitempty" jsonschema:"override task importance"`
	Priority        *string    `json:"priority,omitempty" jsonschema:"override priority"`
	Labels          *[]string  `json:"labels,omitempty" jsonschema:"override labels"`
	Deps            *[]string  `json:"deps,omitempty" jsonschema:"override dependencies"`
	Parent          *string    `json:"parent,omitempty" jsonschema:"override parent"`
	Checks          *[]checkIn `json:"checks,omitempty" jsonschema:"override checks"`
}

type createIn struct {
	Title      string    `json:"title" jsonschema:"task title"`
	Body       string    `json:"body,omitempty" jsonschema:"markdown body / intent"`
	Deps       []string  `json:"deps,omitempty" jsonschema:"task ids that must be closed before this can start"`
	Checks     []checkIn `json:"checks,omitempty" jsonschema:"gate-closing checks"`
	Labels     []string  `json:"labels,omitempty" jsonschema:"free-text labels"`
	Priority   string    `json:"priority,omitempty" jsonschema:"one of: low, medium, high, urgent"`
	Parent     string    `json:"parent,omitempty" jsonschema:"parent task id (epic/sub-task)"`
	ProjectID  *string   `json:"project_id,omitempty" jsonschema:"stable project id; omit only on a project-bound Carbon connection to use its bound project; pass empty only on an explicitly selected cluster scope for a cluster-wide task"`
	Type       string    `json:"type,omitempty" jsonschema:"explicit task type: foundation, library, patch, extension, plugin, or a configured custom type"`
	Importance string    `json:"importance,omitempty" jsonschema:"explicit importance: core, important, normal, optional, or experimental"`
}

type reorderIn struct {
	ID   string  `json:"id" jsonschema:"the task id"`
	Rank float64 `json:"rank" jsonschema:"new ordering rank (use a value between two neighbors)"`
}

type updateIn struct {
	ID              string     `json:"id" jsonschema:"the task id"`
	Priority        *string    `json:"priority,omitempty" jsonschema:"set priority (empty clears); omit to leave unchanged"`
	Labels          *[]string  `json:"labels,omitempty" jsonschema:"set labels (empty clears); omit to leave unchanged"`
	Deps            *[]string  `json:"deps,omitempty" jsonschema:"replace dependency task ids (empty clears); omit to leave unchanged; rejects dangling dependencies and cycles"`
	Parent          *string    `json:"parent,omitempty" jsonschema:"set parent id (empty clears); omit to leave unchanged"`
	Title           *string    `json:"title,omitempty" jsonschema:"set the title (must be non-empty); omit to leave unchanged"`
	Body            *string    `json:"body,omitempty" jsonschema:"set the markdown body; omit to leave unchanged"`
	Checks          *[]checkIn `json:"checks,omitempty" jsonschema:"replace the full checks list (carry result on retained checks); omit to leave unchanged"`
	ProjectID       *string    `json:"project_id,omitempty" jsonschema:"legacy mode only; Carbon rejects this field and requires bulk_move with expected versions, force, and reason"`
	Type            *string    `json:"type,omitempty" jsonschema:"set configured task type"`
	Importance      *string    `json:"importance,omitempty" jsonschema:"set importance independently of priority"`
	Assignee        *string    `json:"assignee,omitempty" jsonschema:"legacy mode only; Carbon rejects this field and requires the lease claim/reassign workflow"`
	ExpectedVersion string     `json:"expected_version,omitempty" jsonschema:"optional raw version or quoted ETag optimistic-concurrency token"`
}

// These proof-management inputs are deliberately v2-only. The frozen v1 update
// schema remains untouched, while Carbon clients get server-owned evidence audit data.
type setBlockerIn struct {
	ID              string `json:"id" jsonschema:"task id"`
	BlockerReason   string `json:"blocker_reason" jsonschema:"blocker explanation; empty clears"`
	ExpectedVersion string `json:"expected_version" jsonschema:"required raw version or quoted ETag optimistic-concurrency token"`
}

type addEvidenceIn struct {
	ID              string `json:"id" jsonschema:"task id"`
	Kind            string `json:"kind" jsonschema:"git_commit, git_url, artifact, test_run, other, or a safe lowercase extension key"`
	Value           string `json:"value" jsonschema:"required proof value, maximum 2048 characters"`
	Label           string `json:"label,omitempty" jsonschema:"optional display label, maximum 256 characters"`
	URL             string `json:"url,omitempty" jsonschema:"optional http or https URL"`
	ExpectedVersion string `json:"expected_version" jsonschema:"required raw version or quoted ETag optimistic-concurrency token"`
}

type removeEvidenceIn struct {
	ID              string `json:"id" jsonschema:"task id"`
	EvidenceID      string `json:"evidence_id" jsonschema:"server-assigned evidence id"`
	ExpectedVersion string `json:"expected_version" jsonschema:"required raw version or quoted ETag optimistic-concurrency token"`
}

type deleteOut struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

type editNoteIn struct {
	ID    string `json:"id" jsonschema:"the task id"`
	Note  string `json:"note,omitempty" jsonschema:"the note's stable id; omit only for a legacy note addressed by index"`
	Index *int   `json:"index,omitempty" jsonschema:"0-based provenance index; used only when the note id is absent"`
	Text  string `json:"text" jsonschema:"the replacement note text"`
}

type deleteNoteIn struct {
	ID    string `json:"id" jsonschema:"the task id"`
	Note  string `json:"note,omitempty" jsonschema:"the note's stable id; omit only for a legacy note addressed by index"`
	Index *int   `json:"index,omitempty" jsonschema:"0-based provenance index; used only when the note id is absent"`
}

type checkIn struct {
	Desc    string `json:"desc" jsonschema:"what the check verifies"`
	Cmd     string `json:"cmd,omitempty" jsonschema:"shell command; omit for a manual check"`
	Type    string `json:"type,omitempty" jsonschema:"set to 'manual' for an attested check"`
	Cwd     string `json:"cwd,omitempty" jsonschema:"working dir relative to repo root"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"timeout in seconds"`
	Result  string `json:"result,omitempty" jsonschema:"carry an existing result (pending|pass|fail) on edit; omit for a new check (defaults pending)"`
}

type transitionIn struct {
	ID string `json:"id" jsonschema:"the task id"`
	To string `json:"to" jsonschema:"the target state"`
}

type runChecksIn struct {
	ID   string `json:"id" jsonschema:"the task id"`
	Only []int  `json:"only,omitempty" jsonschema:"check indices to run; omit to run all"`
}

type noteIn struct {
	ID   string `json:"id" jsonschema:"the task id"`
	Text string `json:"text" jsonschema:"the note text"`
}

type attestIn struct {
	ID    string `json:"id" jsonschema:"the task id"`
	Index int    `json:"index" jsonschema:"0-based index of the manual check to attest"`
	Pass  *bool  `json:"pass,omitempty" jsonschema:"attestation result; omit or true = pass, false = fail"`
}

type listOut struct {
	Tasks []taskOut `json:"tasks"`
}

type createTypeIn struct {
	Key string `json:"key" jsonschema:"lowercase reusable task type key; create sparingly after checking list_types"`
}

type taskTypesOut struct {
	Types  []string               `json:"types"`
	Custom []tasktypes.Definition `json:"custom"`
}

type taskTypeOut struct {
	Definition tasktypes.Definition `json:"definition"`
}

type taskOut struct {
	ID               string              `json:"id"`
	Title            string              `json:"title"`
	Status           string              `json:"status"`
	Assignee         string              `json:"assignee,omitempty"`
	ProjectID        string              `json:"projectId,omitempty"`
	Type             string              `json:"type,omitempty"`
	Importance       string              `json:"importance,omitempty"`
	Version          string              `json:"version,omitempty"`
	Deps             []string            `json:"deps,omitempty"`
	Ready            bool                `json:"ready"`
	UpdatedAt        string              `json:"updatedAt,omitempty"`
	Rank             float64             `json:"rank,omitempty"`
	Labels           []string            `json:"labels,omitempty"`
	Priority         string              `json:"priority,omitempty"`
	Parent           string              `json:"parent,omitempty"`
	BlockerReason    string              `json:"blockerReason,omitempty"`
	Evidence         []task.Evidence     `json:"evidence,omitempty"`
	ActiveAttempt    string              `json:"activeAttempt,omitempty"`
	Lease            *task.Lease         `json:"lease,omitempty"`
	PendingClaims    []task.ClaimRequest `json:"pendingClaims,omitempty"`
	ExecutionState   string              `json:"executionState,omitempty"`
	SessionID        string              `json:"sessionId,omitempty"`
	ActivityHealth   string              `json:"activityHealth"`
	LastMeaningfulAt string              `json:"lastMeaningfulAt,omitempty"`
	StagnantAt       string              `json:"stagnantAt,omitempty"`
	Checks           []checkOut          `json:"checks,omitempty"`
	Provenance       []store.Provenance  `json:"provenance,omitempty"`
	Body             string              `json:"body,omitempty"`
}

type checkOut struct {
	Desc   string `json:"desc"`
	Cmd    string `json:"cmd,omitempty"`
	Type   string `json:"type,omitempty"`
	Result string `json:"result"`
	Cwd    string `json:"cwd,omitempty"`
}

// view builds the full single-task response, computing the derived ready flag.
func (svc *Service) view(doc *store.Doc) taskOut {
	updatedAt := ""
	if n := len(doc.Provenance); n > 0 {
		updatedAt = doc.Provenance[n-1].At
	}
	executionState, sessionID := svc.executionForTask(doc.Task)
	activity := svc.ActivityOf(doc)
	return taskOut{ID: doc.Task.ID, Title: doc.Task.Title, Status: doc.Task.Status,
		Assignee: doc.Task.Assignee, ProjectID: doc.Task.ProjectID, Type: doc.Task.Type, Importance: doc.Task.Importance, Version: doc.Version(),
		Deps: doc.Task.Deps, Ready: svc.ReadyOf(doc.Task),
		UpdatedAt: updatedAt, Rank: doc.Task.Rank, Labels: doc.Task.Labels, Priority: doc.Task.Priority,
		Parent: doc.Task.Parent, BlockerReason: doc.Task.BlockerReason, Evidence: doc.Task.Evidence, ActiveAttempt: doc.Task.ActiveAttempt, ExecutionState: executionState,
		ActivityHealth: activity.Health, LastMeaningfulAt: activity.LastMeaningfulAt, StagnantAt: activity.StagnantAt,
		Lease: doc.Task.Lease, PendingClaims: doc.Task.PendingClaims, SessionID: sessionID, Checks: toCheckOut(doc.Task.Checks), Provenance: doc.Provenance, Body: doc.Body}
}

// ReadyOf computes a task's derived readiness best-effort; on a load error it reports
// false rather than failing the response. Used by the web adapter for single-task DTOs.
func (svc *Service) ReadyOf(t task.Task) bool {
	cfg, err := svc.store.Config()
	if err != nil {
		return false
	}
	// Resolve only this task's listed deps instead of scanning the whole board — this runs on
	// the response path of every single-task endpoint (get/note/update/transition).
	return task.ReadyFunc(t, svc.depResolver(), rulesOf(cfg))
}

func toCheckOut(checks []task.Check) []checkOut {
	if len(checks) == 0 {
		return nil
	}
	out := make([]checkOut, len(checks))
	for i, c := range checks {
		out[i] = checkOut{Desc: c.Desc, Cmd: c.Cmd, Type: c.Type, Result: c.Result, Cwd: c.Cwd}
	}
	return out
}

func fromCheckIn(in []checkIn) []task.Check {
	if len(in) == 0 {
		return nil
	}
	out := make([]task.Check, len(in))
	for i, c := range in {
		result := c.Result
		if result == "" {
			result = "pending"
		}
		out[i] = task.Check{Desc: c.Desc, Cmd: c.Cmd, Type: c.Type, Cwd: c.Cwd, Timeout: c.Timeout, Result: result}
	}
	return out
}

func secondsDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 0 // lease.Manager applies its bounded default.
	}
	return time.Duration(seconds) * time.Second
}

func searchResultsOut(svc *Service, results []search.Result) searchOut {
	out := searchOut{Results: make([]searchResultOut, 0, len(results))}
	for _, result := range results {
		if result.Doc == nil {
			continue
		}
		out.Results = append(out.Results, searchResultOut{
			ClusterID: result.ClusterID, Task: svc.view(result.Doc), Score: result.Score, Highlights: result.Highlights,
		})
	}
	return out
}

func taskDocsOut(svc *Service, docs []*store.Doc) taskListOut {
	out := taskListOut{Tasks: make([]taskOut, 0, len(docs))}
	for _, doc := range docs {
		if doc != nil {
			out.Tasks = append(out.Tasks, svc.view(doc))
		}
	}
	return out
}

func templateFromTool(in templateWriteIn) templates.Template {
	return templates.Template{
		ID: in.ID, Name: in.Name, Title: in.Title, Body: in.Body,
		ProjectID: in.ProjectID, ClusterWide: in.ClusterWide,
		Type: in.Type, Importance: in.Importance, Priority: in.Priority,
		Labels: CloneStrings(in.Labels), Deps: CloneStrings(in.Deps),
		Checks: fromCheckIn(in.Checks), Parent: in.Parent,
	}
}

func checksPointer(in *[]checkIn) *[]task.Check {
	if in == nil {
		return nil
	}
	checks := fromCheckIn(*in)
	return &checks
}
