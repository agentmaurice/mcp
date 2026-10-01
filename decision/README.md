# Decision MCP

Classify short, already extracted content, score it on an ordered scale, or ask
a yes/no question using TypeSafe System One. Receive typed values and confidence
without parsing generated prose. This server never executes a branch or writes
to another system.

Use it for page classification, quality scoring, urgency checks, or several
independent questions about one input. Extract files/images with another tool
first; use a generative model for free-form extraction, explanations or summaries.
Validate confidence on labelled data from your own workload before automating
consequential actions.

## Run

Requires Go 1.26 and a TypeSafe API credential. Evaluation requests leave the
host and incur TypeSafe usage. Health/capabilities make no provider calls.

```sh
go build -o bin/decision ./cmd/decision
export MCP_DECISION_TYPESAFE_API_KEY_FILE=/secure/typesafe.key
./bin/decision
```

STDIO is the default. The file contains only the raw key. Alternatively, use
`MCP_DECISION_TYPESAFE_API_KEY`; setting both is rejected. Keep credentials out
of tool arguments, source control and shared MCP launch definitions.

Coding-agent MCP configuration (replace paths):

```json
{"mcpServers":{"decision":{"command":"/absolute/path/bin/decision","env":{"MCP_DECISION_TYPESAFE_API_KEY_FILE":"/secure/typesafe.key"}}}}
```

For Streamable HTTP and legacy SSE, set `MCP_DECISION_TRANSPORT=http`. Endpoints:
`/mcp`, `/sse`, `/health`, `/ready`. HTTP has no application authentication: keep
it on loopback or behind your authenticated private ingress. Health confirms
local readiness, not that TypeSafe has accepted the credential.

| Variable | Default | Meaning |
|---|---|---|
| `MCP_DECISION_TYPESAFE_API_KEY` | unset | Provider credential |
| `MCP_DECISION_TYPESAFE_API_KEY_FILE` | unset | Alternative credential file |
| `MCP_DECISION_HOSTED_KEY` | unset | Organization-scoped decisions usage key; used only without TypeSafe BYOK |
| `MCP_DECISION_HOSTED_URL` | `https://llm.agentmaurice.app` | Hosted gateway base URL; the relay calls `/v1/decisions` on it |
| `MCP_DECISION_HOSTED_INSTANCE_ID` | unset | Optional connected-instance identity when the hosted key is that instance’s secret |
| `MCP_DECISION_TYPESAFE_URL` | `https://api.typesafe.ai` | Base URL or full `/v1/systemone`; HTTP only on loopback. `https://openrouter.ai/api` with an OpenRouter key works unchanged (same request and response shapes) |
| `MCP_DECISION_MODEL` | `jev-latest` | Model id; `typesafe/jev-1.13` on OpenRouter, `hosted:jev-latest` or a pinned `hosted:jev-1.13` on the hosted route |
| `MCP_DECISION_TIMEOUT` | `5s` | Total deadline including retries |
| `MCP_DECISION_MAX_STATE_BYTES` | `32768` | Compact JSON byte limit, may be lowered |
| `MCP_DECISION_TRANSPORT` | `stdio` | `stdio` or `http` |
| `MCP_DECISION_HTTP_ADDR` | `127.0.0.1:8080` | HTTP bind address |

## Tools and contract

| Tool | Arguments | Use |
|---|---|---|
| `decision_health_v1` | `{}` | Check local readiness without spending tokens |
| `decision_capabilities_v1` | `{}` | Discover types, bounds and model |
| `decision_ask_v1` | `state`, `questions` | Batch 1–16 questions in one provider call |
| `decision_choice_v1` | `state`, `instructions`, `criteria`, optional `min_confidence` | Select a category |
| `decision_score_v1` | Same fields as choice, with an ordered criteria list | Score ordered levels |
| `decision_noul_v1` | `state`, `instructions`, optional `true_means`, `false_means` | Probability of yes |

State is a JSON string, object or array. Instructions contain 1–500 characters.
Choice criteria are a map of 2–255 ids to nonempty descriptions. Score criteria
are a list of 2–16 nonempty descriptions; indices start at zero. Question and
choice ids follow `^[a-z][a-z0-9]*(?:[-_][a-z0-9]+)*$`; `uncertain` is reserved.

Example `decision_ask_v1` arguments:

```json
{
  "state": "Travel mug, €24.90. In stock. Add to cart.",
  "questions": {
    "kind": {
      "type": "choice",
      "instructions": "What is the primary purpose of this page?",
      "criteria": {"article": "Editorial content", "product": "Item offered for purchase"},
      "min_confidence": 0.8
    },
    "urgent": {"type": "noul", "instructions": "Does it require action within 24 hours?"}
  }
}
```

Batch results contain `answers` under the same ids, plus `provider`, `model` and
`input_tokens`. Shortcut tools return the answer directly. Each answer has
`type`, `confidence_kind`, `via_fallback`, and the type-specific fields:

- Choice: `choice`, `probabilities`, `confidence`.
- Score: `score`, `legend`, `probabilities`, `confidence`.
- Noul: `noul` (0–1); no separate confidence value is invented.

The provider sets `confidence_kind` (`calibrated` for TypeSafe); authors cannot
set it. Confidence is not necessarily the probability of the chosen class.
Below `min_confidence`, choice returns `uncertain`, score returns `null`, and
`via_fallback` becomes true. Confidence and probabilities remain available for
diagnosis. Equality with the threshold passes. Noul accepts no threshold.

Malformed/inconsistent provider responses fail. Distributions must contain the
declared keys and sum to one within `0.0001`; scores must match the weighted
level distribution within the same tolerance. 401/422 are not retried. Only
429/529 are retried, at most three attempts within the total deadline. Redirects
are not followed. Errors do not echo provider bodies, state or credentials.
There is no generative-model fallback.

## Tests

```sh
go test -race ./... -count=3
go test -tags=integration ./integration -v
```

Live tests are skipped unless `MCP_DECISION_TYPESAFE_API_KEY` is set in the test
process. They send only committed synthetic fixtures and incur provider usage.
The report is `artifacts/integration/typesafe-report.json`, or the directory in
`MCP_DECISION_REPORT_DIR`. It contains accuracy, confidence buckets, Brier score,
latency and input tokens. This small smoke dataset does not establish statistical
calibration on customer data.

References: [TypeSafe API](https://docs.typesafe.ai/api.md),
[confidence semantics](https://docs.typesafe.ai/confidence.md).

License: Apache-2.0.

## Hosted decisions

Set `MCP_DECISION_HOSTED_KEY` to the organization's dedicated decisions usage
key issued by Console. The relay calls `POST /v1/decisions` on the gateway: a
route named by category, not by vendor, that serves the AgentMaurice decision
contract (`state`, typed `questions`, `answers` carrying `confidence_kind`).
The gateway names the resolved provider's confidence kind and the relay keeps
it as is; the vendor route stays `/v1/systemone`. The organization wallet pays
for input tokens only, with the same credit formula as hosted LLM calls.
TypeSafe BYOK takes precedence when configured.
The gateway handles bounded provider retries, so the relay does not multiply
those retries. A request ID is stable across transport attempts; a duplicate
logical submission is rejected without another debit. Do not reuse a GoModel
service key: it is not an organization-scoped System One credential.

Hosted requests send `state` to the upstream decision provider (TypeSafe,
United States, reached through the gateway's routing provider). Choose BYOK or
avoid the hosted route when that processing location does not meet your needs.
Provider metadata reports `agentmaurice` on this route. Health and capabilities
do not call the provider or spend tokens.
