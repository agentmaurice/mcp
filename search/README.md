# MCP Search

AgentMaurice MCP server for hybrid search on Meilisearch. It returns passages
and their provenance, without an LLM answer.

## Scope v0.1

The binary runs over STDIO, bound by the operator to a principal, an
organization, a deployment and a corpus with homogeneous access rights. It must
not be shared between users with different rights. Identity never comes from
MCP arguments; HTTP mode is refused.

Meilisearch is separate. Search uses a key limited to `search` and to its
index, never a master key. Connectors, OCR, chunking and index administration
remain external. Multi-user sharing awaits a proof of identity and runtime
grants.

## Getting started

Clone this repository; `shared/` is a sibling module resolved through
`go.work`. Then use Go 1.26:

```sh
go build -trimpath -o bin/search ./cmd/search
SEARCH_CONFIG_FILE=/path/search.json \
SEARCH_API_KEY_FILE=/path/search-key \
MCP_TRANSPORT=stdio ./bin/search
```

Mount the files read-only and restrict their access to the process owner.
`search-key` contains only a Meilisearch search key or a restricted tenant
token. Missing files or an empty identity prevent startup. Example
configuration to adapt:

```json
{
  "meili_url":"http://meilisearch:7700",
  "organization_id":"org-a",
  "deployment_id":"dep-a",
  "principal_id":"alice",
  "corpus":"manual",
  "index_uid":"manual_v1",
  "index_revision":"v1",
  "embedder":"documents"
}
```

The trusted launcher reserves this process for the designated principal. A
`principal_id` string typed by a user does not authenticate them. Do not
expose the binary through a shared MCP proxy. HTTP is fine for the backend on
an isolated private network; use HTTPS elsewhere. No proxy from the
environment and no HTTP redirect is followed. Without an embedder, only
`keyword` is available.

## Tools

| Tool | Arguments | Result |
|------|-----------|--------|
| `search_query_v1` | `corpus`, `query`, `mode?`, `limit?` | Ranked passages and provenance |
| `search_get_v1` | `corpus`, `passage_id` | Passage with the same access filters |
| `search_health_v1` | none | Engine availability, not index freshness |
| `search_capabilities_v1` | none | Allowed corpus, modes and limits |

```json
{"corpus":"manual","query":"comment résilier un fournisseur","mode":"hybrid","limit":5}
```

Modes: `keyword`, `semantic`, `hybrid` (default). `semanticRatio` is 0.5 in
hybrid and 1 in semantic. Without an embedder, the hybrid default returns
`mode_unavailable`, with no silent fallback. Query: 4 KiB maximum; 5 results
by default, 20 maximum; passage: 16 KiB; structured response: 256 KiB; backend
timeout: 10 seconds. Unknown fields, null values and invalid limits are
refused.

Response: `status`, `results`, `mode`, `index_revision`, `truncated`.
A passage contains `id`, `document_id`, `title`, `content`, `source_ref`,
`position`, `source_revision`. The content is a text excerpt to escape when
displayed, never HTML to execute. `truncated` signals an additional result
beyond the limit; an oversized passage causes an error.

Errors: `invalid_argument`, `scope_required`, `not_found`, `mode_unavailable`,
`backend_unavailable`, `response_too_large`. Raw backend errors and
credentials never leak out. `get` uses a search filtered by identifier, not
the documents API, which ignores tenant token restrictions.

## Corpus preparation and lifecycle

The index producer prepares passages of this shape:

```json
{
  "id":"contract-1","document_id":"contract","title":"Contrat fournisseur",
  "content":"Notifier la résiliation trente jours avant échéance.",
  "source_ref":"storage:contract","position":"section-4","source_revision":"r1",
  "organization_id":"org-a","deployment_id":"dep-a","corpus":"manual","index_revision":"v1"
}
```

Identifiers use ASCII letters, digits, `_`, `-`, up to 128 characters.
`source_ref` is opaque, with no signed URL or secret. The content is untrusted
data for the Agent.

With a separate administration credential, the producer must:

1. Create the index with `id` as the primary key; declare `title`, `content`
   as searchable and `id`, `organization_id`, `deployment_id`, `corpus`,
   `index_revision` as filterable.
2. Configure the embedder and its template `{{doc.title}} {{doc.content}}`,
   then send the passages. Meilisearch generates their embeddings; no double
   computation in RAG for this corpus.
3. Wait for `succeeded` for each `taskUid` via `GET /tasks/{taskUid}`;
   `failed` or `canceled` forbids publishing the revision.
4. Create a key with `actions: ["search"]`, `indexes: ["manual_v1"]` and an
   explicit expiration; mount it in the dedicated process.
5. Publish the configuration of the ready revision and start Search.

A change of model, template or corpus is prepared in a new revision. Stop the
reader during an in-place modification. For a deletion or a withdrawal of
rights, suspend access before indexing and reopen it after success. The
producer remains responsible for this freshness; Search does not synchronize
the source system's ACLs itself.

Meilisearch 1.53.2 blocks outbound private IPs by default. For a local
embedder, allow only its IP/CIDR with
`MEILI_EXPERIMENTAL_ALLOWED_IP_NETWORKS`. The test allows only the `/32` of
the synthetic `embedder` service.

## Verification

```sh
go vet ./...
go test ./...
sh scripts/integration-local.sh
```

The last script starts two ephemeral containers pinned by digest, on an
isolated network, with the Meilisearch port bound to loopback. It tests the
real STDIO binary: discovery, four tools, keyword/semantic/hybrid FR/EN,
filtering, citation, update, deletion and scope withdrawal. Automatic cleanup,
no Docker build/push and no paid call. The synthetic provider validates the
protocol, not the relevance of a real document model.

## Delivery

CI runs these tests, including a real Meilisearch. The non-root sidecar image
is published on SemVer tags only. Creating the MCP does not activate any client
instance.

## License

Apache-2.0.
