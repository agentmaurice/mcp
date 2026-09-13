#!/bin/bash

# Script pour ingérer les données de test dans le système RAG
# Usage: ./scripts/ingest_test_data.sh [tenant_id]
#
# Le tenant_id peut être n'importe quelle chaîne.
# L'API retournera un tenant_id xid valide à utiliser pour les requêtes.

set -e

TENANT_ID=${1:-"test-tenant"}
API_URL=${RAG_API_URL:-"http://localhost:8084"}
TESTDATA_DIR="testdata/documents"

# Variable pour stocker le tenant_id xid retourné par l'API
ACTUAL_TENANT_ID=""

echo "🚀 Ingestion des données de test"
echo "=================================="
echo "Tenant ID initial: $TENANT_ID"
echo "API URL: $API_URL"
echo ""

# Fonction pour ingérer un fichier
ingest_file() {
    local file=$1
    local filename=$(basename "$file")
    local title="${filename%.*}"

    echo "📄 Ingestion de $filename..."

    # Lecture du contenu du fichier
    content=$(cat "$file")

    # Utiliser le tenant_id xid si déjà obtenu, sinon utiliser l'original
    local use_tenant_id="${ACTUAL_TENANT_ID:-$TENANT_ID}"

    # Création du payload JSON
    payload=$(jq -n \
        --arg tenant_id "$use_tenant_id" \
        --arg title "$title" \
        --arg content "$content" \
        --arg source_type "markdown" \
        --arg url "file://$file" \
        '{
            tenant_id: $tenant_id,
            title: $title,
            content: $content,
            source_type: $source_type,
            source_url: $url
        }')

    # Appel à l'API d'ingestion
    response=$(curl -s -X POST "$API_URL/api/ingest" \
        -H "Content-Type: application/json" \
        -d "$payload")

    job_id=$(echo "$response" | jq -r '.job_id')
    returned_tenant_id=$(echo "$response" | jq -r '.tenant_id')

    if [ "$job_id" != "null" ] && [ -n "$job_id" ]; then
        echo "✅ Job créé: $job_id"
        echo "   Tenant ID (xid): $returned_tenant_id"

        # Sauvegarder le tenant_id pour les requêtes futures
        if [ -z "$ACTUAL_TENANT_ID" ]; then
            ACTUAL_TENANT_ID="$returned_tenant_id"
        fi

        # Attendre que le job soit terminé
        while true; do
            status_response=$(curl -s "$API_URL/api/ingest/$job_id")
            status=$(echo "$status_response" | jq -r '.status')
            progress=$(echo "$status_response" | jq -r '.progress')

            echo "   Status: $status ($progress%)"

            if [ "$status" = "completed" ]; then
                echo "✅ Ingestion terminée pour $filename"
                break
            elif [ "$status" = "failed" ]; then
                message=$(echo "$status_response" | jq -r '.message')
                echo "❌ Échec de l'ingestion: $message"
                break
            fi

            sleep 2
        done
    else
        echo "❌ Erreur lors de la création du job"
        echo "$response" | jq '.'
    fi

    echo ""
}

# Vérifier que le répertoire existe
if [ ! -d "$TESTDATA_DIR" ]; then
    echo "❌ Répertoire $TESTDATA_DIR introuvable"
    exit 1
fi

# Ingérer tous les fichiers .md du répertoire
for file in "$TESTDATA_DIR"/*.md; do
    if [ -f "$file" ]; then
        ingest_file "$file"
    fi
done

echo "✅ Ingestion des données de test terminée!"
echo ""
if [ -n "$ACTUAL_TENANT_ID" ]; then
    echo "📌 Tenant ID (xid) à utiliser pour les requêtes: $ACTUAL_TENANT_ID"
    echo ""
    echo "Pour tester une requête, utilisez:"
    echo "curl -X POST $API_URL/api/query \\"
    echo "  -H 'Content-Type: application/json' \\"
    echo "  -d '{\"tenant_id\": \"$ACTUAL_TENANT_ID\", \"query\": \"Qu'est-ce que le RAG?\"}'"
else
    echo "⚠️  Aucun fichier n'a été ingéré"
fi
