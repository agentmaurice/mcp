# AgentMaurice Social MCP

Go MCP server that drafts, schedules and publishes social posts through
[Postiz](https://postiz.com). Postiz (AGPL-3.0) stays a **connected service**:
either Postiz Cloud or the customer's own instance. This MCP **hosts no Postiz**
and contains no Postiz code. It is only an HTTP client of Postiz's public REST
API, written from the public documentation.

## Tools

| Tool | Purpose |
| --- | --- |
| `social_health_v1` | `{status:"ok", configured:true, version}`; without URL and key `{status:"not_configured", configured:false, version}` |
| `social_capabilities_v1` | configured or not, operations, limits, client rate, idempotency scope |
| `social_channels_v1` | `{channels:[{id, provider, name, picture?, disabled}]}` |
| `social_post_create_v1` | `channels` (1..10), `content` (<= 5000), `media` (<= 4 images), `mode` (`draft`, `schedule`, `now`), `schedule_at` (RFC 3339, at least 120 s ahead, rechecked just before the POST; required for `schedule`), `idempotency_key` |
| `social_post_get_v1` | `{post_id, state, channels:[{id, state, url?, error?}]}` |
| `social_post_delete_v1` | removes the post from the schedule or unpublishes it |

Errors are `isError` results `{error, message}` with a stable code:
`invalid_arguments`, `not_configured`, `auth_failed`, `rate_limited`
(with `retry_after_s`), `unknown_channel`, `channel_disabled`,
`schedule_at_invalid`, `media_too_large`, `media_type_refused`,
`buffer_unavailable`, `buffer_not_found`, `idempotency_conflict`,
`post_not_found`, `upstream_invalid_response`, `upstream_error`.

## Configuration (sidecar environment only)

| Variable | Default | Role |
| --- | --- | --- |
| `SOCIAL_POSTIZ_URL` | none | Public API base, e.g. `https://api.postiz.com/public/v1` |
| `SOCIAL_POSTIZ_API_KEY` | none | API key (secret, at least 16 characters: a shorter one is refused with `not_configured`, reason `api_key_too_short`), sent as the `Authorization` header; never logged, never returned |
| `SOCIAL_BUFFER_SERVICE_URL`, `SOCIAL_BUFFER_TOKEN`, `SOCIAL_BUFFER_NAMESPACE` | none | Buffer for media `ref` (`buffer://...`); `BUFFER_*` aliases accepted |

The client limiter is fixed at 30 requests per hour (sliding window) over all Postiz
calls and is not configurable. `Retry-After` (seconds or HTTP date) is bounded to 1 s
to 24 h; negative, unreadable or larger values give 3600 s.

Without the two Postiz variables every tool except health and capabilities
answers `not_configured`. Media can be passed as `content_base64` or as a Buffer
`ref` (`buffer_unavailable` when no Buffer is configured). Accepted types:
JPEG, PNG, GIF, WebP; the content is sniffed and must match the declared type.
Images are uploaded first, then referenced by the post. All validation happens
before the first upload or post. A refusal that happens after the uploads (for example
a schedule that became too close) can leave uploaded media in Postiz, since its API
offers no way to delete them. The image size cap is fixed at 20 MiB.

## Behaviour notes

- `idempotency_key`: the same key and request returns the first result
  (`idempotent_replay: true`) with no second post; the same key with another
  request is `idempotency_conflict`. The replay is answered before any validation,
  so a scheduled post replayed after its date returns the stored result. The memory is **per process**, bounded (1000
  keys, 24 h) and lost on restart: it is not a durable guarantee.
- `mode:"now"` is accepted by Postiz and published through its queue: the result
  state is `scheduled` with a note; read `social_post_get_v1` for the final state.
- `social_post_get_v1` can additionally return `state:"error"`. Postiz exposes no
  single-post read: the tool lists posts over a window (90 days back, 365 ahead, widened to the known planned date)
  and filters by id. For a multi-channel create, the sibling ids are known only
  while the process lives; after a restart only the given id is read. The planned
  date of posts created by this process is remembered, so the window always
  contains it; an id unknown to the process is searched over 90 days back and 5 years
  ahead, and `not_found_in_window` (with the bounds) is returned if it is not found.
- `social_post_delete_v1` deletes every post of the same group (Postiz behaviour).

## Failure semantics

- One rule decides the outcome of `POST /posts` once it has left (`decideCreate`).
  Conclusive answers: a `postId` for every requested channel (success), or an
  explicit no-effect refusal (4xx, 401/403, 429, local rate limit), which frees the
  key. Anything else is memorized under the idempotency key and never re-sent on
  replay: transport error, 2xx with an unreadable, truncated or empty body, any 5xx
  or other status give `outcome_unknown` (check the Postiz schedule, or send a new
  `idempotency_key` to republish); some channels proven and others not give
  `partial_failure`, listing each channel as created (`scheduled`/`draft`) or
  `missing`, with no `status:"ok"`. The group is derived from the key, but the list
  API exposes no group field, so the doubt cannot be lifted automatically.
- `social_post_get_v1`: when the sibling ids of a group are known (same process), a
  channel missing from the list is reported `state:"unknown"`; the global state is
  `published` only if every channel is, and `partial` when some are unknown; a channel whose creation was not confirmed (partial creation) is reported `state:"unknown"` (never `missing`: nothing proves it was not created), also forcing `partial`. If the list holds a post on that channel with the planned date of the group (Postiz exposes no group field, so this is a heuristic), it is reported with its id and state and `inferred:true`; an inferred post is informative only: for the global state its channel still counts as `unknown`, so it can never make the state `published`.
- `social_post_get_v1` on an id this process did not create returns
  `not_found_in_window` with `window_start` and `window_end`: Postiz has no
  single-post read, so absence from the window does not prove absence.

## Global state of `social_post_get_v1`

Aggregation of the per-channel states (the per-channel list is authoritative):
one common state gives that state; otherwise any `error`, `missing` or `unknown`
channel gives `partial`; otherwise `mixed` (for example `published` and `draft`).

## Secrets

`SOCIAL_POSTIZ_API_KEY` (and the Buffer token) are masked as `[redacted]` in every
result, including error messages and the `error` field of channels coming from
Postiz. The server writes no log of requests or bodies.

## Postiz API: what was verified

Checked on the official documentation on 2026-11-07:

- Base URL, key in the `Authorization` header without a prefix, endpoints,
  limits: <https://docs.postiz.com/public-api/introduction> (cloud base
  `https://api.postiz.com/public/v1`).
- `GET /integrations` fields `id, name, identifier, picture, disabled, profile`:
  <https://docs.postiz.com/public-api/integrations/list.md>.
- `POST /posts` body (`type`, `date`, `shortLink`, `tags`, `posts[].integration.id`,
  `value[].content`, `value[].image[]{id,path}`, `group`, `settings.__type`) and
  response `[{postId, integration}]`: <https://docs.postiz.com/public-api/posts/create.md>.
- `GET /posts?startDate&endDate` with `state` in `QUEUE|PUBLISHED|ERROR|DRAFT`,
  `releaseURL`, `error`: <https://docs.postiz.com/public-api/posts/list.md>.
- `DELETE /posts/{id}` deletes the whole group:
  <https://docs.postiz.com/public-api/posts/delete.md>; `DELETE /posts/group/{group}`:
  <https://docs.postiz.com/public-api/posts/delete-by-group.md>.
- `POST /upload`, multipart field `file`, response `{id, name, path}`; image types
  and MP4 only, PDF refused: <https://docs.postiz.com/public-api/uploads/upload-file.md>.
- X settings need `who_can_reply_post`: <https://docs.postiz.com/public-api/providers/x.md>.

Hypotheses, not verifiable without a key:

- Self-hosted base URL: the docs give `https://{your-domain}/api/public/v1` in the
  OpenAPI pages and `{NEXT_PUBLIC_BACKEND_URL}/public/v1` in the introduction;
  the operator sets the full base in `SOCIAL_POSTIZ_URL`.
- Rate: the docs disagree (30 requests per hour on every endpoint page; 100 on
  Cloud and 90 self-hosted, create-post only, on the introduction). The client
  limiter defaults to the stricter 30 and counts every call.
- `Retry-After` on 429 is not documented; without it `retry_after_s` is 3600.
- Settings of providers other than `x` are sent as `{__type}` only; providers with
  mandatory settings (YouTube, Pinterest, TikTok...) will be refused by Postiz
  (`upstream_error`) until per-provider settings are added.
- 401 vs 403 on a bad key (both map to `auth_failed`), and 404 on deleting an
  unknown id (mapped to `post_not_found`).
- A group id chosen by the client in `posts[].group` is accepted by Postiz.

## Development

`task check` (gofmt, `go vet`, `go test ./...`, build). Tests run against an
`httptest` fake of the documented API; no Postiz key is needed. The sidecar image
is built by the GitLab pipeline (`Dockerfile.sidecar`, context = the `mcp`
directory, `SIDECAR_IMAGE` build argument).

## License

Proprietary. Postiz is a separate AGPL-3.0 product that this project does not
include or distribute.

Redirects (3xx) are never followed: they become `upstream_redirect` (code only, no
destination) and every request sent is counted by the client limiter. Post ids
returned by Postiz must be distinct per channel; a duplicate, or a missing or
unknown `integration`, counts as not proven (`partial_failure` or `outcome_unknown`).

A 200 answer to `GET /posts` or `GET /integrations` whose body is not the documented
form (`{"posts":[...]}` or an array: `{}`, `null`, a missing `posts`, unreadable JSON)
is `upstream_invalid_response`, never `post_not_found` or `not_found_in_window`.
