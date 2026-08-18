// Package inventoryclient provides an HTTP client for bbo-stock-api's inventory
// endpoints, replacing the former in-process pkg/inventory and pkg/inventory_harian
// packages. bbo-stock-api is the single owner of all inventory domain code.
//
// Auth: requests are signed with the GATEWAY_SHARED_SECRET (same HMAC-SHA256
// scheme the API gateway uses to sign requests to downstream services).
// This allows service-to-service calls without forwarding user JWTs.
//
// Read methods (GetCurrentBalance, GetBalanceAt) are synchronous and
// non-transactional — safe to call directly.
//
// Write methods (AddInventory, UpdateInventory, DeleteInventory) MUST be
// called AFTER the main GORM transaction commits. They are idempotent
// (AddInventory checks t_ref_id) so a retry on failure is safe.
// Use the OpCollector to defer writes until after commit.
package inventoryclient

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	configs "github.com/natifdevelopment/go-config"
)

const (
	stockAPIURLEnvKey       = "STOCK_API_URL"
	defaultStockAPIURL      = "http://localhost:8096"
	inventoryClientTimeout  = 30 * time.Second
)

// Client is the HTTP client for bbo-stock-api inventory endpoints.
type Client struct {
	baseURL string
	client  *http.Client
}

var defaultClient *Client

// GetClient returns a singleton client instance configured from STOCK_API_URL.
func GetClient() *Client {
	if defaultClient == nil {
		baseURL := os.Getenv(stockAPIURLEnvKey)
		if baseURL == "" {
			baseURL = defaultStockAPIURL
		}
		defaultClient = &Client{
			baseURL: baseURL,
			client:  &http.Client{Timeout: inventoryClientTimeout},
		}
	}
	return defaultClient
}

// NewClient creates a new client with an explicit base URL.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		client:  &http.Client{Timeout: inventoryClientTimeout},
	}
}

// --- Inventory (t_inventory) ---

// GetCurrentBalance fetches the latest inventory record for an organization.
func (cl *Client) GetCurrentBalance(c *gin.Context, orgId uuid.UUID) (*InventoryBalance, error) {
	url := fmt.Sprintf("%s/api/v1/inventory/current-balance/%s", cl.baseURL, orgId)
	return cl.doGetBalance(url)
}

// GetBalanceAt fetches the inventory balance at a specific date.
func (cl *Client) GetBalanceAt(c *gin.Context, orgId uuid.UUID, at time.Time) (*InventoryBalance, error) {
	url := fmt.Sprintf("%s/api/v1/inventory/balance-at/%s/%s", cl.baseURL, orgId, at.UTC().Format(time.RFC3339))
	return cl.doGetBalance(url)
}

// AddInventory creates an inventory record via HTTP. Must be called after tx commit.
func (cl *Client) AddInventory(c *gin.Context, params UpsertInventoryParams) error {
	return cl.doPost("/api/v1/inventory/add", upsertBody(params))
}

// UpdateInventory updates an inventory record via HTTP. Must be called after tx commit.
func (cl *Client) UpdateInventory(c *gin.Context, params UpsertInventoryParams) error {
	return cl.doPost("/api/v1/inventory/update", upsertBody(params))
}

// DeleteInventory removes an inventory record via HTTP. Must be called after tx commit.
func (cl *Client) DeleteInventory(c *gin.Context, params DeleteInventoryParams) error {
	return cl.doPost("/api/v1/inventory/delete", deleteBody(params))
}

// --- InventoryHarian (t_inventory_harian) ---

// GetCurrentBalanceHarian fetches the latest inventory harian record.
func (cl *Client) GetCurrentBalanceHarian(c *gin.Context, orgId uuid.UUID) (*InventoryBalance, error) {
	url := fmt.Sprintf("%s/api/v1/inventory-harian/current-balance/%s", cl.baseURL, orgId)
	return cl.doGetBalance(url)
}

// GetBalanceAtHarian fetches the inventory harian balance at a specific date.
func (cl *Client) GetBalanceAtHarian(c *gin.Context, orgId uuid.UUID, at time.Time) (*InventoryBalance, error) {
	url := fmt.Sprintf("%s/api/v1/inventory-harian/balance-at/%s/%s", cl.baseURL, orgId, at.UTC().Format(time.RFC3339))
	return cl.doGetBalance(url)
}

// AddInventoryHarian creates an inventory harian record via HTTP.
func (cl *Client) AddInventoryHarian(c *gin.Context, params UpsertInventoryParams) error {
	return cl.doPost("/api/v1/inventory-harian/add", upsertBody(params))
}

// UpdateInventoryHarian updates an inventory harian record via HTTP.
func (cl *Client) UpdateInventoryHarian(c *gin.Context, params UpsertInventoryParams) error {
	return cl.doPost("/api/v1/inventory-harian/update", upsertBody(params))
}

// DeleteInventoryHarian removes an inventory harian record via HTTP.
func (cl *Client) DeleteInventoryHarian(c *gin.Context, params DeleteInventoryParams) error {
	return cl.doPost("/api/v1/inventory-harian/delete", deleteBody(params))
}

// --- HTTP helpers ---

func upsertBody(params UpsertInventoryParams) map[string]interface{} {
	body := map[string]interface{}{
		"organizationId":  params.OrganizationId,
		"transactionDate": params.TransactionDate,
		"amount":          params.Amount,
		"transactionType": params.TransactionType,
		"referenceId":     params.ReferenceId,
	}
	if params.TargetId != nil {
		body["targetId"] = *params.TargetId
	}
	return body
}

func deleteBody(params DeleteInventoryParams) map[string]interface{} {
	body := map[string]interface{}{
		"organizationId": params.OrganizationId,
		"referenceId":    params.ReferenceId,
	}
	if params.TargetId != nil {
		body["targetId"] = *params.TargetId
	}
	return body
}

func (cl *Client) doGetBalance(url string) (*InventoryBalance, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("inventory client: create request: %w", err)
	}
	cl.signRequest(req)

	resp, err := cl.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("inventory client: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &InventoryBalance{Balance: 0}, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("inventory client: stock-api returned %d: %s", resp.StatusCode, string(body))
	}

	var apiResp struct {
		Status bool             `json:"status"`
		Data   InventoryBalance `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("inventory client: decode response: %w", err)
	}
	return &apiResp.Data, nil
}

func (cl *Client) doPost(path string, body map[string]interface{}) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("inventory client: marshal body: %w", err)
	}
	url := cl.baseURL + path
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("inventory client: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	cl.signRequest(req)

	resp, err := cl.client.Do(req)
	if err != nil {
		return fmt.Errorf("inventory client: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("inventory client: stock-api returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// signRequest adds the gateway HMAC-SHA256 signature headers so that
// stock-api's AuthGuard accepts the service-to-service call.
// Signature = HMAC-SHA256(secret, method + "\n" + path + "\n" + timestamp)
func (cl *Client) signRequest(req *http.Request) {
	secret := configs.GATEWAY_SHARED_SECRET
	if secret == "" {
		return
	}
	tsStr := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(req.Method + "\n" + req.URL.Path + "\n" + tsStr))
	sig := hex.EncodeToString(mac.Sum(nil))
	req.Header.Set("X-Gateway-Signature", sig)
	req.Header.Set("X-Gateway-Timestamp", tsStr)
}
