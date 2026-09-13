package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelfRegisterSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/mcp/self-register", r.URL.Path)
		require.Equal(t, "Bearer bootstrap-token", r.Header.Get("Authorization"))

		var req SelfRegisterRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "public-key", req.PublicKey)
		require.Equal(t, "localmaurice", req.Metadata["source"])

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mcp_id":"mcp-1","api_key":"api-1","tenant_id":"tenant-1","deployment_id":"tenant-1","mqtt_broker":"tcp://host.docker.internal:1883"}`))
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL)
	resp, err := c.SelfRegister(context.Background(), "bootstrap-token", &SelfRegisterRequest{
		PublicKey: "public-key",
		Metadata:  map[string]string{"source": "localmaurice"},
	})
	require.NoError(t, err)
	require.Equal(t, "mcp-1", resp.MCPID)
	require.Equal(t, "api-1", resp.APIKey)
	require.Equal(t, "tenant-1", resp.TenantID)
	require.Equal(t, "tenant-1", resp.DeploymentID)
	require.Equal(t, "tcp://host.docker.internal:1883", resp.MQTTBroker)
}

func TestSelfRegisterErrors(t *testing.T) {
	t.Run("http error with api message", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"invalid bootstrap token"}`))
		}))
		defer srv.Close()

		c := NewAPIClient(srv.URL)
		_, err := c.SelfRegister(context.Background(), "bad-token", &SelfRegisterRequest{})
		require.ErrorContains(t, err, "invalid bootstrap token")
	})

	t.Run("invalid json response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{`))
		}))
		defer srv.Close()

		c := NewAPIClient(srv.URL)
		_, err := c.SelfRegister(context.Background(), "token", &SelfRegisterRequest{})
		require.ErrorContains(t, err, "decode response")
	})
}

func TestValidateCredentials(t *testing.T) {
	t.Run("non 200 means invalid", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "Bearer api-key", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		c := NewAPIClient(srv.URL)
		resp, err := c.ValidateCredentials(context.Background(), "api-key")
		require.NoError(t, err)
		require.False(t, resp.Valid)
	})

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/api/v1/mcp/auth/validate", r.URL.Path)
			_, _ = w.Write([]byte(`{"valid":true,"renew_required":true}`))
		}))
		defer srv.Close()

		c := NewAPIClient(srv.URL)
		resp, err := c.ValidateCredentials(context.Background(), "api-key")
		require.NoError(t, err)
		require.True(t, resp.Valid)
		require.True(t, resp.RenewRequired)
	})

	t.Run("invalid json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{`))
		}))
		defer srv.Close()

		c := NewAPIClient(srv.URL)
		_, err := c.ValidateCredentials(context.Background(), "api-key")
		require.ErrorContains(t, err, "decode response")
	})
}

func TestRenewCredentials(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/api/v1/mcp/auth/renew", r.URL.Path)
			require.Equal(t, "Bearer old-api-key", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"api_key":"new-api-key"}`))
		}))
		defer srv.Close()

		c := NewAPIClient(srv.URL)
		resp, err := c.RenewCredentials(context.Background(), "old-api-key")
		require.NoError(t, err)
		require.Equal(t, "new-api-key", resp.APIKey)
	})

	t.Run("error with api message", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"cannot renew"}`))
		}))
		defer srv.Close()

		c := NewAPIClient(srv.URL)
		_, err := c.RenewCredentials(context.Background(), "old-api-key")
		require.ErrorContains(t, err, "cannot renew")
	})

	t.Run("invalid json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{`))
		}))
		defer srv.Close()

		c := NewAPIClient(srv.URL)
		_, err := c.RenewCredentials(context.Background(), "old-api-key")
		require.ErrorContains(t, err, "decode response")
	})
}
