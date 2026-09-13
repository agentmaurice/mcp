#!/bin/bash

# Script pour tester une requête RAG
# Usage: ./scripts/test_query.sh "Ma question" [tenant_id]

set -e

QUERY=${1:-"Qu'est-ce que le RAG?"}
TENANT_ID=${2:-"test-tenant-001"}
API_URL=${RAG_API_URL:-"http://localhost:8084"}

echo "🔍 Test de requête RAG"
echo "======================"
echo "Query: $QUERY"
echo "Tenant ID: $TENANT_ID"
echo "API URL: $API_URL"
echo ""

# Création du payload JSON
payload=$(jq -n \
    --arg tenant_id "$TENANT_ID" \
    --arg query "$QUERY" \
    '{
        tenant_id: $tenant_id,
        query: $query,
        max_tokens: 500
    }')

# Appel à l'API de requête
echo "📡 Envoi de la requête..."
response=$(curl -s -X POST "$API_URL/api/query" \
    -H "Content-Type: application/json" \
    -d "$payload")

echo ""
echo "📝 Réponse:"
echo "==========="
echo "$response" | jq -r '.answer'

echo ""
echo "📚 Citations:"
echo "============="
echo "$response" | jq -r '.citations[] | "- \(.snippet)"'
