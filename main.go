package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"sync"
)

// Transaction struct to hold transaction details
type Transaction struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Value string `json:"value"`
	Hash  string `json:"hash"`
}

// EthereumClient to interact with Ethereum JSON-RPC
type EthereumClient struct {
	rpcURL     string
	httpClient *http.Client
}

func NewEthereumClient(rpcURL string) *EthereumClient {
	return &EthereumClient{
		rpcURL:     rpcURL,
		httpClient: &http.Client{},
	}
}

func (ec *EthereumClient) GetCurrentBlockNumber() (int, error) {
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_blockNumber",
		"params":  []interface{}{},
		"id":      1,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	resp, err := ec.httpClient.Post(ec.rpcURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return 0, err
	}

	blockHex := result["result"].(string)
	blockNumber, err := strconv.ParseInt(blockHex[2:], 16, 64) // Convert hex to int
	if err != nil {
		return 0, err
	}

	return int(blockNumber), nil
}

func (ec *EthereumClient) GetBlockByNumber(blockNumber string) (map[string]interface{}, error) {
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_getBlockByNumber",
		"params":  []interface{}{blockNumber, true}, // `true` for full transaction objects
		"id":      1,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := ec.httpClient.Post(ec.rpcURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return nil, err
	}

	if result["error"] != nil {
		return nil, fmt.Errorf("RPC error: %v", result["error"])
	}
	return result["result"].(map[string]interface{}), nil
}

// ParserService manages subscriptions and transactions
type ParserService struct {
	client         *EthereumClient
	subscribedAddr map[string]bool
	mu             sync.Mutex
}

func NewParserService(client *EthereumClient) *ParserService {
	return &ParserService{
		client:         client,
		subscribedAddr: make(map[string]bool),
	}
}

func (ps *ParserService) GetCurrentBlock() int {
	blockNumber, err := ps.client.GetCurrentBlockNumber()
	if err != nil {
		log.Printf("Error getting current block: %v", err)
		return 0
	}
	return blockNumber
}

func (ps *ParserService) Subscribe(address string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.subscribedAddr[address] {
		return false
	}
	ps.subscribedAddr[address] = true
	return true
}

func (ps *ParserService) GetTransactions(address string) []Transaction {
	// Step 1: Get the current block number
	blockNumber := ps.GetCurrentBlock()

	// Step 2: Get block details by number
	blockDetails, err := ps.client.GetBlockByNumber(fmt.Sprintf("0x%x", blockNumber))
	if err != nil {
		log.Printf("Error getting block details: %v", err)
		return nil
	}

	// Log the block details to see if data is returned
	log.Printf("Block Details: %+v", blockDetails)

	// Step 3: Filter transactions related to the given address
	var transactions []Transaction
	for _, tx := range blockDetails["transactions"].([]interface{}) {
		transaction := tx.(map[string]interface{})
		from := transaction["from"].(string)
		to := transaction["to"].(string)

		// Check if the address matches the 'from' or 'to' address
		if from == address || to == address {
			transactions = append(transactions, Transaction{
				From:  from,
				To:    to,
				Value: transaction["value"].(string),
				Hash:  transaction["hash"].(string),
			})
		}
	}

	// Step 4: Return filtered transactions
	return transactions
}

// Utility to validate Ethereum addresses
func isValidEthereumAddress(address string) error {
	if len(address) != 42 {
		return fmt.Errorf("address must be 42 characters long")
	}
	if address[:2] != "0x" {
		return fmt.Errorf("address must start with '0x'")
	}
	isHex := regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`).MatchString
	if !isHex(address) {
		return fmt.Errorf("address contains invalid characters")
	}
	return nil
}

// Handlers
func handleGetBlock(w http.ResponseWriter, r *http.Request, ps *ParserService) {
	currentBlock := ps.GetCurrentBlock()

	blockDetails, err := ps.client.GetBlockByNumber(fmt.Sprintf("0x%x", currentBlock))
	if err != nil {
		http.Error(w, fmt.Sprintf("Error fetching block details: %v", err), http.StatusInternalServerError)
		return
	}

	transactions := blockDetails["transactions"].([]interface{})
	response := map[string]interface{}{
		"blockNumber":  currentBlock,
		"transactions": transactions,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handleSubscribe(w http.ResponseWriter, r *http.Request, ps *ParserService) {
	// First check if address is provided in the headers
	address := r.Header.Get("address")

	// If not found in headers, check query parameters
	if address == "" {
		address = r.URL.Query().Get("address")
	}

	if address == "" {
		http.Error(w, "Address not provided", http.StatusBadRequest)
		return
	}

	// Validate the Ethereum address
	if err := isValidEthereumAddress(address); err != nil {
		http.Error(w, fmt.Sprintf("Invalid address: %s", err.Error()), http.StatusBadRequest)
		return
	}

	// Subscribe to the address
	success := ps.Subscribe(address)
	if success {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Subscribed to address: %s\n", address)
	} else {
		http.Error(w, "Already subscribed", http.StatusBadRequest)
	}
}

func handleGetTransactions(w http.ResponseWriter, r *http.Request, ps *ParserService) {
	// Extract address from query parameters or headers
	address := r.URL.Query().Get("address")
	if address == "" {
		http.Error(w, "Address not provided", http.StatusBadRequest)
		return
	}

	// Validate the Ethereum address
	if err := isValidEthereumAddress(address); err != nil {
		http.Error(w, fmt.Sprintf("Invalid address: %s", err.Error()), http.StatusBadRequest)
		return
	}

	// Get the transactions for the address
	transactions := ps.GetTransactions(address)

	// Respond with the list of transactions
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(transactions)
}

// Main function
func main() {
	client := NewEthereumClient("https://ethereum-rpc.publicnode.com")
	parserService := NewParserService(client)

	http.HandleFunc("/getBlock", func(w http.ResponseWriter, r *http.Request) {
		handleGetBlock(w, r, parserService)
	})
	http.HandleFunc("/subscribe", func(w http.ResponseWriter, r *http.Request) {
		handleSubscribe(w, r, parserService)
	})
	http.HandleFunc("/getTransactions", func(w http.ResponseWriter, r *http.Request) {
		handleGetTransactions(w, r, parserService)
	})

	log.Println("Server is running on port 8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
