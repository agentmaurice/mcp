# AgentMaurice CMS MCP

Go MCP server that creates, updates, schedules and unpublishes posts and pages,
and uploads images, on a **connected CMS**. The first adapter is **WordPress**
(self-hosted or any site that exposes the core REST API). The CMS stays a
connected service owned by the customer: this MCP **hosts no CMS**, contains no
WordPress code and needs **no WordPress plugin**. It is only an HTTP client of the
native REST API `/wp-json/wp/v2` (posts, pages, media), authenticated with the
Application Passwords of WordPress core, written from the public documentation.
One adapter per CMS: Ghost and others come later behind the same tools.

## Tools

| Tool | Purpose |
| --- | --- |
| `cms_health_v1` | `{status:"ok", configured:true, version}`; without configuration `{status:"not_configured", configured:false, reason?, version}` |
| `cms_capabilities_v1` | adapter configured or not (no value), operations, statuses, limits, idempotency scope, error codes |
| `cms_post_create_v1` | `type` (`post` default, or `page`), `title`, `content` (HTML), `excerpt`, `slug`, `categories` and `tags` (term ids, posts only), `status` (`draft` default, `publish`, `future`), `date` (RFC 3339, required for `future`, in the future), `featured_media` (media id), `idempotency_key` (required) |
| `cms_media_upload_v1` | `content_base64` or `buffer_ref` (exactly one), `filename`, `mime_type`, `alt_text`, `idempotency_key` (required); returns `{media_id, url, link, mime_type}` |
| `cms_post_get_v1` | `post_id`, optional `type`; returns `{post_id, type, state, link, date, date_gmt, modified, slug}` |
| `cms_post_update_v1` | `post_id`, optional `type`, the same fields as create (only the given ones change), `idempotency_key` (required) |
| `cms_post_unpublish_v1` | `post_id`, optional `type`, `mode` = `draft` (status back to draft) or `trash` (DELETE without `force`), `idempotency_key` (required) |

Deviation from the brief, on purpose: `get`, `update` and `unpublish` take an
**optional `type`** because posts and pages live on different REST collections.
When it is omitted the MCP reads `/posts/{id}` then `/pages/{id}` (ids are unique
across types in WordPress) and uses the one that exists.

Results are `{status:"ok", post_id, type, state, link, date, modified, slug}`; `state`
is the WordPress status actually stored (`note` is added when it differs from the
one requested). Unpublishing **never deletes permanently**: there is no `force`
code path, and `trash` keeps the item recoverable in the WordPress trash.

Errors are structured results `{error, message}` with a stable code. Business
errors do **not** set the MCP `isError` flag (constant `businessErrorIsError` in
`internal/mcpserver/server.go`; note that `mcp/social` currently sets it to `true`,
flip the constant to match). Codes: `invalid_arguments`, `not_configured`,
`auth_failed`, `forbidden`, `rate_limited` (with `retry_after_s`), `post_not_found`,
`media_too_large`, `media_type_refused`, `buffer_unavailable`, `buffer_not_found`,
`idempotency_conflict`, `wordpress_error`, `rest_api_unavailable`,
`trash_not_supported`, `upstream_invalid_response`, `upstream_redirect`,
`outcome_unknown`, `upstream_error`. WordPress refusals carry `wp_status` and
`wp_code` (for example `rest_cannot_create`).

The One adds `deployment_id`, `organization_id`, `user_id`, `space_id` and
`session_id` to every tool call: they are removed before the strict validation
(`additionalProperties` is otherwise refused with `invalid_arguments`).

## Configuration (sidecar environment only)

All values are secrets of the sidecar. They are never a tool argument and never
part of a prompt.

| Variable | Role |
| --- | --- |
| `CMS_WORDPRESS_URL` | Site base, for example `https://blog.example.com` (a sub-path such as `https://example.com/blog` is accepted). **https is required**; plain `http` is accepted only for a loopback test host (`localhost`, `*.localhost`, `127.0.0.0/8`, `::1`). No credentials, query or fragment in the URL. Refusals: `not_configured` with reason `url_not_https` or `url_invalid`. |
| `CMS_WORDPRESS_USER` | WordPress user name that owns the application password (at least 4 characters: a shorter one is refused with `not_configured`, reason `user_too_short`) |
| `CMS_WORDPRESS_APP_PASSWORD` | Application Password (at least 16 characters, WordPress generates 24; the spaces it displays are accepted) |
| `BUFFER_SERVICE_URL`, `BUFFER_TOKEN`, `BUFFER_NAMESPACE` | Buffer for media `buffer_ref` (`buffer://...`); `CMS_BUFFER_*` aliases accepted |

Without the three WordPress variables every tool except health and capabilities
answers `not_configured` (with the **names** of the missing variables, never a
value). The configuration check comes before any argument check.

The HTTPS rule matches WordPress itself: Application Passwords are available "to
all sites using SSL or to local environments" only (see the sources below), and
Basic credentials in clear over a network are refused here regardless.

### Masking

The application password (also without its spaces), its Basic token and the Buffer
token are replaced by `[redacted]` by substring in every string value of every
result, error or not (never in the keys of the structure). No minimum length
applies: the configuration refuses a user under 4 characters and a password under
16 instead.

The WordPress user name is an identifier, not a secret in the strong sense, so it
is **not** masked by substring in a success (a user named `publish` must not turn
`state: "publish"` into `[redacted]`). It is masked when it is the whole value of
a field outside the contract enums (`status`, `state`, `type`, `mode`, `error`,
`mime_type`, `version`), and by substring in the `message` of an error. In an error
`message` the site base URL is masked too. The links WordPress returns in a
success (`link`, `url`) are kept: they are the purpose of the tool. The server
writes no log of requests, headers or bodies. The credentials only travel in the
`Authorization` header.

## Behaviour notes

- `idempotency_key` (create, update, media upload and unpublish): the same key and the same request return
  the first result (`idempotent_replay: true`) with no second call to WordPress;
  the same key with another request is `idempotency_conflict`. The replay is
  answered before validation, so a scheduled post replayed after its date returns
  the stored result. The memory is **per process**, bounded (1000 keys, 24 h) and
  **lost at restart**: after a restart the same key sends the request again, so it
  is not a durable guarantee (WordPress has no idempotency header). A clear refusal
  (4xx) frees the key.
- Writes are serialized in the process.
- `date` is sent as `date_gmt` (UTC). `publish` with a future date is refused:
  use `future`. A dated draft is allowed.
- Media: JPEG, PNG, GIF, WebP; the content is sniffed and must match the declared
  type; the file name is ASCII (`[A-Za-z0-9._-]`, at most 100 characters). Size cap:
  20 MiB (the WordPress/PHP limit may be lower and then answers an error). The
  image is sent first as a raw body, then `alt_text` is set with a second call
  (the media is always `status:ok` with its id; `alt_text_set` is `true` only when WordPress answers with this media id and the requested text (compared after whitespace normalization, since WordPress sanitizes it), `false` when it certainly refused (4xx), or `"unknown"` when the call went out and failed or the answer does not confirm the text (connection cut, 5xx, invalid or divergent answer), with a `warning`). Whatever the site `unfiltered_html` rights, WordPress may sanitize the
  HTML `content` for users without that capability.
- Pretty permalinks must be on: `/wp-json` is the only route used (not
  `?rest_route=`).

## Failure semantics

- Timeouts: 30 s per request, 120 s for a media upload. Redirects (3xx) are never
  followed and become `upstream_redirect` (code only, no destination); a redirect
  proves nothing was executed and frees the key. Answers are read up to 4 MiB; a
  larger or non-JSON answer is `upstream_invalid_response` on a read.
- **Writes** (create, update, unpublish, media): a clear 4xx refusal, a redirect or
  the documented `501 rest_trash_not_supported` proves that nothing changed. A
  network failure after the request left, a 5xx, or a 2xx with an unreadable body
  gives `outcome_unknown`, **never** `failed`. A 2xx counts as a success only if it carries every expected field with a valid type (post, page or trashed item: `id` > 0, `type`, `status`, `link`, `date`, `modified`; media: `id` > 0, `source_url`, `link`, `mime_type`); and only if it concerns the target (same `id` for update and unpublish, same `type` when known, `type` `attachment` for a media); and only if the returned status is the requested one (create and update with a `status`; unpublish `draft` needs `draft`, `trash` needs `trash`; a requested `future` may come back `publish` when its date has passed meanwhile); otherwise the write is treated as left with an unknown outcome: check WordPress (drafts and trash
  included) before retrying. For create and update the unknown outcome is
  memorized under the key and **not re-sent on replay**; a new key sends it again.
- `unpublish` `trash` on an item already in the trash returns `ok` with
  `already_trashed:true` (WordPress answers `410 rest_already_trashed`).
- `trash_not_supported` (HTTP 501) is reported as is: this MCP never falls back to
  `force=true`.

## WordPress REST API: what was verified

Checked on the official documentation on 2026-11-08:

- **Authentication.** Application Passwords ship with WordPress core since 5.6, are
  created on the Edit User screen, and are sent with HTTP Basic (RFC 7617) over
  https: <https://developer.wordpress.org/rest-api/using-the-rest-api/authentication/>
  (the user name and the application password go in the Basic `Authorization` header).
  Spaces in the displayed password are optional; availability follows SSL or a
  local environment, and the `wp_is_application_passwords_available` filter:
  <https://make.wordpress.org/core/2020/11/05/application-passwords-integration-guide/>,
  <https://developer.wordpress.org/reference/functions/wp_is_application_passwords_available/>
  ("available to all sites using SSL or to local environments").
- **Statuses and dates.** `status` accepts `publish`, `future`, `draft`, `pending`,
  `private`; `date` (site time zone) and `date_gmt` (GMT); `slug`, `title`,
  `content`, `excerpt`, `categories`, `tags` (arrays of term ids), `featured_media`
  (integer); response fields `id`, `link`, `status`, `date`, `modified`:
  <https://developer.wordpress.org/rest-api/reference/posts/>.
- **Media upload.** `POST /wp/v2/media` with the raw file as body, `Content-Type`
  the MIME type and `Content-Disposition: attachment; filename="image.png"`;
  without them WordPress answers `rest_upload_no_content_type`,
  `rest_upload_no_content_disposition` or `rest_upload_invalid_disposition` (400):
  <https://developer.wordpress.org/reference/classes/wp_rest_attachments_controller/upload_from_data/>.
  Fields `alt_text`, `source_url`, `mime_type`, `link`:
  <https://developer.wordpress.org/rest-api/reference/media/>.
- **Trash and unpublish.** `DELETE /wp/v2/posts/<id>` moves to the trash unless
  `force=true` ("Whether to bypass Trash and force deletion"); `rest_trash_not_supported`
  (501) and `rest_already_trashed` (410):
  <https://developer.wordpress.org/rest-api/reference/posts/>,
  <https://developer.wordpress.org/reference/classes/wp_rest_posts_controller/delete_item/>.
  Back to draft is an update of `status`.
- **Categories and tags.** Posts reference terms by id; terms are managed on
  `/wp/v2/categories` and `/wp/v2/tags`:
  <https://developer.wordpress.org/rest-api/reference/categories/>. This MCP takes
  ids only and creates no term.
- **Slug.** An alphanumeric identifier unique to its type (posts reference above).
  WordPress may append a suffix when it is taken: the stored `slug` is returned.
- **Pagination.** `per_page` capped at 100, `page`, `offset`, headers `X-WP-Total`
  and `X-WP-TotalPages`:
  <https://developer.wordpress.org/rest-api/using-the-rest-api/pagination/>. No tool
  lists a collection in v1.
- **Errors.** A `WP_Error` becomes `{"code","message","data":{"status"}}` (a 4xx or
  5xx HTTP status):
  <https://developer.wordpress.org/rest-api/extending-the-rest-api/adding-custom-endpoints/>.

Hypotheses, not stated by those pages (covered by the real-WordPress test below):

- Ids are unique across posts and pages, and `GET /posts/{id}` on a page answers
  `404 rest_post_invalid_id`; the lookup order relies on it.
- A trashed item is still readable by `GET` for a user who can edit it.
- `Retry-After` on 429 is not documented for WordPress; without it
  `retry_after_s` is 3600 (bounded 1 s to 24 h otherwise).
- `date_gmt` alone is enough to schedule (`status: future`).
- `X-WP-*` rate limits do not exist in core: only a proxy or a security plugin
  answers 429.

## Development

`task check` (gofmt, `go vet`, `go test ./...`, build). `go.mod` has
`replace .../mcp/shared => ../shared`; the CI checks `shared` out beside this
repository at `MCP_SHARED_REF`. The unit tests run against an `httptest` fake of the
documented API and need no WordPress. The sidecar image is built by the GitLab
pipeline (`Dockerfile.sidecar`, context = the `mcp` directory, `SIDECAR_IMAGE`
build argument), never locally.

### Test against a real WordPress (optional)

`TestIntegrationRealWordPress` is skipped unless `CMS_WP_INTEGRATION_URL`,
`CMS_WP_INTEGRATION_USER` and `CMS_WP_INTEGRATION_APP_PASSWORD` are set. It uploads
an image, creates a draft with it, replays the key, publishes (and fetches the
public link), goes back to draft, schedules, trashes (and checks the item is still
there), and creates a page.

```sh
docker compose -f docker/compose.yaml up -d
eval "$(sh docker/wp-setup.sh)"      # installs WordPress with wp-cli, creates an application password
go test -race -count=1 -run Integration -v ./internal/mcpserver
docker compose -f docker/compose.yaml down -v
```

`docker/compose.yaml` is a disposable WordPress + MariaDB bound to
`127.0.0.1:8080` (site declared `WP_ENVIRONMENT_TYPE=local`, which is what allows
Application Passwords over plain http on `localhost`). `docker/wp-setup.sh` prints
only the three `export` lines on stdout. Inside this repository's worktree layout
use `go -modfile` with a copy of `go.mod` whose `replace` points to the absolute
path of `shared`.

## License

Proprietary. WordPress is a separate GPL product that this project does not include
or distribute.
