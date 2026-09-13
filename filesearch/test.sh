#!/bin/bash

# MCP FileSearch Test Script
# This script tests the complete workflow of the FileSearch MCP server

set -e

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Configuration
BASE_URL="http://localhost:8080/mcp/filesearch"
DEPLOYMENT_ID="test-deployment"
SESSION_ID=$(uuidgen)

echo -e "${BLUE}=== MCP FileSearch Test Script ===${NC}"
echo -e "Session ID: ${SESSION_ID}"
echo ""

# Helper function to call MCP tools
call_tool() {
    local tool_name=$1
    local arguments=$2
    local id=$3

    echo -e "${BLUE}[${id}] Calling tool: ${tool_name}${NC}"

    response=$(curl -s -X POST "${BASE_URL}/message?sessionId=${SESSION_ID}" \
        -H "Content-Type: application/json" \
        -d "{
            \"jsonrpc\": \"2.0\",
            \"id\": ${id},
            \"method\": \"tools/call\",
            \"params\": {
                \"name\": \"${tool_name}\",
                \"arguments\": ${arguments}
            }
        }")

    echo "$response" | jq '.'
    echo ""
}

# Test 1: Create store
echo -e "${GREEN}Step 1: Creating FileSearch store...${NC}"
call_tool "init_or_repair_store" \
    "{\"deployment_id\": \"${DEPLOYMENT_ID}\", \"display_name\": \"Test Store\"}" \
    1

# Test 2: Upload local file (README.md)
echo -e "${GREEN}Step 2: Uploading README.md...${NC}"
call_tool "upload_local_file" \
    "{\"deployment_id\": \"${DEPLOYMENT_ID}\", \"file_path\": \"./README.md\", \"display_name\": \"README\"}" \
    2

# Test 3: Get store info
echo -e "${GREEN}Step 3: Getting store info...${NC}"
call_tool "get_store_info" \
    "{\"deployment_id\": \"${DEPLOYMENT_ID}\"}" \
    3

# Test 4: List files
echo -e "${GREEN}Step 4: Listing files...${NC}"
call_tool "list_files" \
    "{\"deployment_id\": \"${DEPLOYMENT_ID}\"}" \
    4

# Test 5: Wait for indexing
echo -e "${BLUE}Waiting 30 seconds for file indexing...${NC}"
sleep 30

# Test 6: Perform semantic search
echo -e "${GREEN}Step 5: Performing semantic search...${NC}"
call_tool "semantic_query" \
    "{\"deployment_id\": \"${DEPLOYMENT_ID}\", \"query\": \"What is MCP FileSearch?\"}" \
    5

# Test 7: Get usage stats
echo -e "${GREEN}Step 6: Getting usage statistics...${NC}"
call_tool "get_usage_stats" \
    "{\"deployment_id\": \"${DEPLOYMENT_ID}\"}" \
    6

# Test 8: Health check
echo -e "${GREEN}Step 7: Running health check...${NC}"
call_tool "health_check" \
    "{}" \
    7

echo -e "${GREEN}=== All tests completed! ===${NC}"
