#!/bin/bash
# Mock MCP server that returns Atlassian-like tools

while IFS= read -r line; do
    # Skip empty lines
    [ -z "$line" ] && continue
    
    # Parse the JSON-RPC request
    method=$(echo "$line" | grep -o '"method":"[^"]*"' | cut -d'"' -f4)
    req_id=$(echo "$line" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
    
    if [ "$method" = "initialize" ]; then
        echo "{\"jsonrpc\":\"2.0\",\"id\":\"$req_id\",\"result\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{\"tools\":{}},\"serverInfo\":{\"name\":\"atlassian-mcp\",\"version\":\"1.0\"}}}"
    elif [ "$method" = "tools/list" ]; then
        echo "{\"jsonrpc\":\"2.0\",\"id\":\"$req_id\",\"result\":{\"tools\":[{\"name\":\"jira_create_issue\",\"description\":\"Create a new JIRA issue\",\"inputSchema\":{\"type\":\"object\",\"properties\":{\"project\":{\"type\":\"string\"},\"summary\":{\"type\":\"string\"}},\"required\":[\"project\",\"summary\"]}},{\"name\":\"jira_get_issue\",\"description\":\"Get a JIRA issue by key\",\"inputSchema\":{\"type\":\"object\",\"properties\":{\"issueKey\":{\"type\":\"string\"}},\"required\":[\"issueKey\"]}},{\"name\":\"confluence_create_page\",\"description\":\"Create a new Confluence page\",\"inputSchema\":{\"type\":\"object\",\"properties\":{\"space\":{\"type\":\"string\"},\"title\":{\"type\":\"string\"}},\"required\":[\"space\",\"title\"]}}]}}"
    fi
    # Add newline
    echo ""
done
