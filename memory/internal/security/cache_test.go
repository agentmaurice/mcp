package security

import (
	"fmt"
	"testing"
)

func TestValidationCache_HitMiss(t *testing.T) {
	cache := NewValidationCache(10)
	rules := PortableSQLRules{AllowSelectStar: true}
	sql := "SELECT * FROM v_entities"

	// Miss
	if _, ok := cache.Get(sql, rules); ok {
		t.Fatal("expected cache miss")
	}

	// Put
	info := &SQLInfo{Tables: []string{"v_entities"}}
	cache.Put(sql, rules, ValidationResult{Info: info})

	// Hit
	result, ok := cache.Get(sql, rules)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if result.Info == nil || len(result.Info.Tables) != 1 {
		t.Fatal("expected cached SQLInfo")
	}
	if result.Err != nil {
		t.Fatal("expected no error in cached result")
	}
}

func TestValidationCache_ErrorCached(t *testing.T) {
	cache := NewValidationCache(10)
	rules := PortableSQLRules{}
	sql := "INVALID SQL"

	cache.Put(sql, rules, ValidationResult{Err: fmt.Errorf("parse error")})

	result, ok := cache.Get(sql, rules)
	if !ok {
		t.Fatal("expected cache hit for error")
	}
	if result.Err == nil {
		t.Fatal("expected cached error")
	}
}

func TestValidationCache_DifferentRules(t *testing.T) {
	cache := NewValidationCache(10)
	sql := "SELECT * FROM v_entities"
	rules1 := PortableSQLRules{AllowSelectStar: true}
	rules2 := PortableSQLRules{AllowSelectStar: false}

	cache.Put(sql, rules1, ValidationResult{Info: &SQLInfo{Tables: []string{"v_entities"}}})
	cache.Put(sql, rules2, ValidationResult{Err: fmt.Errorf("select star not allowed")})

	r1, ok1 := cache.Get(sql, rules1)
	r2, ok2 := cache.Get(sql, rules2)

	if !ok1 || r1.Err != nil {
		t.Fatal("rules1 should have valid result")
	}
	if !ok2 || r2.Err == nil {
		t.Fatal("rules2 should have error result")
	}
}

func TestValidationCache_Eviction(t *testing.T) {
	cache := NewValidationCache(3)
	rules := PortableSQLRules{}

	for i := 0; i < 5; i++ {
		sql := fmt.Sprintf("SELECT %d", i)
		cache.Put(sql, rules, ValidationResult{Info: &SQLInfo{}})
	}

	// Oldest entries (0 and 1) should be evicted
	if _, ok := cache.Get("SELECT 0", rules); ok {
		t.Fatal("expected SELECT 0 to be evicted")
	}
	if _, ok := cache.Get("SELECT 1", rules); ok {
		t.Fatal("expected SELECT 1 to be evicted")
	}
	// Newest entries should remain
	if _, ok := cache.Get("SELECT 4", rules); !ok {
		t.Fatal("expected SELECT 4 to be cached")
	}
}

func TestValidationCache_DuplicatePut(t *testing.T) {
	cache := NewValidationCache(10)
	rules := PortableSQLRules{}
	sql := "SELECT 1"

	cache.Put(sql, rules, ValidationResult{Info: &SQLInfo{Tables: []string{"a"}}})
	cache.Put(sql, rules, ValidationResult{Info: &SQLInfo{Tables: []string{"b"}}}) // duplicate, should be ignored

	result, ok := cache.Get(sql, rules)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(result.Info.Tables) != 1 || result.Info.Tables[0] != "a" {
		t.Fatalf("expected original value preserved, got: %v", result.Info.Tables)
	}
}
