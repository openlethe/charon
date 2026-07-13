package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: mcp-test <obol-token>")
		os.Exit(1)
	}

	token := os.Args[1]
	ctx := context.Background()

	// Create streamable HTTP client
	c, err := client.NewStreamableHttpClient("http://localhost:18484/mcp", transport.WithHTTPHeaders(map[string]string{
		"Authorization": "Bearer " + token,
	}))
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	defer c.Close()

	// Initialize
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "test-client",
		Version: "1.0.0",
	}

	result, err := c.Initialize(ctx, initReq)
	if err != nil {
		log.Fatalf("Initialize failed: %v", err)
	}
	fmt.Printf("Connected to %s v%s\n", result.ServerInfo.Name, result.ServerInfo.Version)

	// List tools
	toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		log.Fatalf("ListTools failed: %v", err)
	}
	fmt.Printf("Available tools (%d):\n", len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		fmt.Printf("  - %s: %s\n", tool.Name, tool.Description)
	}

	// Test memory_search
	fmt.Println("\nTesting memory_search...")
	searchReq := mcp.CallToolRequest{}
	searchReq.Params.Name = "memory_search"
	searchReq.Params.Arguments = map[string]interface{}{
		"query": "test",
		"limit": 5,
	}

	searchResult, err := c.CallTool(ctx, searchReq)
	if err != nil {
		log.Fatalf("memory_search failed: %v", err)
	}
	for _, content := range searchResult.Content {
		if text, ok := content.(mcp.TextContent); ok {
			fmt.Printf("Result: %s\n", text.Text)
		}
	}

	fmt.Println("\nAll tests passed!")
}
