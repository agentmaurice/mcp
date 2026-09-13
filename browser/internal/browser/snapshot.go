package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

const snapshotExtractionScript = `
(function(options) {
  const maxElements = Number.isFinite(options.maxElements) && options.maxElements > 0 ? options.maxElements : 200;
  const includeHidden = options.includeHidden === true;

  function esc(value) {
    const str = String(value || '');
    if (window.CSS && typeof window.CSS.escape === 'function') {
      return window.CSS.escape(str);
    }
    return str.replace(/["\\]/g, '\\$&');
  }

  function text(value) {
    return String(value || '').replace(/\s+/g, ' ').trim();
  }

  function roleFromTag(el) {
    const tag = (el.tagName || '').toLowerCase();
    if (tag === 'a' && el.getAttribute('href')) return 'link';
    if (tag === 'button') return 'button';
    if (tag === 'input') {
      const type = (el.getAttribute('type') || 'text').toLowerCase();
      if (type === 'checkbox') return 'checkbox';
      if (type === 'radio') return 'radio';
      if (type === 'submit' || type === 'button') return 'button';
      return 'textbox';
    }
    if (tag === 'textarea') return 'textbox';
    if (tag === 'select') return 'combobox';
    if (tag === 'label') return 'label';
    return '';
  }

  function isVisible(el) {
    if (includeHidden) return true;
    if (!el || !(el instanceof Element)) return false;
    const style = window.getComputedStyle(el);
    if (!style || style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') {
      return false;
    }
    const rect = el.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  }

  function getName(el) {
    const ariaLabel = text(el.getAttribute('aria-label'));
    if (ariaLabel) return ariaLabel;

    const labelledBy = text(el.getAttribute('aria-labelledby'));
    if (labelledBy) {
      const parts = labelledBy
        .split(/\s+/)
        .map((id) => document.getElementById(id))
        .filter(Boolean)
        .map((node) => text(node.textContent))
        .filter(Boolean);
      if (parts.length) return parts.join(' ');
    }

    if (el.labels && el.labels.length) {
      const labels = Array.from(el.labels).map((label) => text(label.textContent)).filter(Boolean);
      if (labels.length) return labels.join(' ');
    }

    const placeholder = text(el.getAttribute('placeholder'));
    if (placeholder) return placeholder;

    const title = text(el.getAttribute('title'));
    if (title) return title;

    const alt = text(el.getAttribute('alt'));
    if (alt) return alt;

    return text(el.innerText || el.textContent);
  }

  function uniqueSelector(el) {
    if (!el || !(el instanceof Element)) return '';

    if (el.id) {
      const candidate = '#' + esc(el.id);
      if (document.querySelectorAll(candidate).length === 1) return candidate;
    }

    const testid = el.getAttribute('data-testid') || el.getAttribute('data-test-id');
    if (testid) {
      const candidate = '[data-testid="' + esc(testid) + '"]';
      if (document.querySelectorAll(candidate).length === 1) return candidate;
    }

    const name = el.getAttribute('name');
    const tag = el.tagName.toLowerCase();
    if (name && ['input', 'select', 'textarea'].includes(tag)) {
      const candidate = tag + '[name="' + esc(name) + '"]';
      if (document.querySelectorAll(candidate).length === 1) return candidate;
    }

    const path = [];
    let current = el;
    while (current && current.nodeType === Node.ELEMENT_NODE && current !== document.body) {
      let part = current.tagName.toLowerCase();
      if (current.id) {
        part += '#' + esc(current.id);
        path.unshift(part);
        break;
      }

      let index = 1;
      let sibling = current;
      while ((sibling = sibling.previousElementSibling) !== null) {
        if (sibling.tagName === current.tagName) index += 1;
      }
      part += ':nth-of-type(' + index + ')';
      path.unshift(part);
      current = current.parentElement;
    }

    return path.join(' > ');
  }

  const selectorSet = new Set();
  const elements = [];
  const candidates = Array.from(document.querySelectorAll(
    'a[href],button,input,select,textarea,summary,[role],[tabindex]:not([tabindex="-1"]),[contenteditable="true"],[onclick],label'
  ));

  for (const el of candidates) {
    if (!isVisible(el)) continue;

    const selector = uniqueSelector(el);
    if (!selector || selectorSet.has(selector)) continue;
    selectorSet.add(selector);

    const role = text(el.getAttribute('role')).toLowerCase() || roleFromTag(el);
    const type = text(el.getAttribute('type')).toLowerCase();
    const name = getName(el);
    const placeholder = text(el.getAttribute('placeholder'));
    const testid = text(el.getAttribute('data-testid') || el.getAttribute('data-test-id'));
    const title = text(el.getAttribute('title'));
    const alt = text(el.getAttribute('alt'));
    const elementText = text(el.innerText || el.textContent).slice(0, 160);

    elements.push({
      selector,
      role,
      name,
      tag: (el.tagName || '').toLowerCase(),
      type,
      text: elementText,
      placeholder,
      test_id: testid,
      title,
      alt
    });
  }

  return {
    url: window.location.href,
    title: document.title || '',
    total: elements.length,
    truncated: elements.length > maxElements,
    elements: elements.slice(0, maxElements)
  };
})
`

// Snapshot captures semantic interactive elements from the current page.
func (c *Client) Snapshot(ctx context.Context, req shared.SnapshotRequest) (*shared.SnapshotResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.connected {
		return nil, shared.ErrServiceUnavailable("browser")
	}

	maxElements := req.MaxElements
	if maxElements <= 0 {
		maxElements = 200
	}

	format := req.Format
	if format == "" {
		format = "compact"
	}

	options := map[string]interface{}{
		"maxElements":   maxElements,
		"includeHidden": req.IncludeHidden,
	}
	script := fmt.Sprintf("(%s)(%s)", snapshotExtractionScript, toJSON(options))

	var raw map[string]interface{}
	if err := c.runWithRetry(ctx, c.config.DefaultTimeout, "snapshot", func(opCtx context.Context) error {
		return chromedp.Run(opCtx, chromedp.Evaluate(script, &raw))
	}); err != nil {
		return nil, shared.ErrBrowserAction("snapshot", err)
	}

	rawElements, _ := raw["elements"].([]interface{})
	elements := make([]shared.SnapshotElement, 0, len(rawElements))
	for i, item := range rawElements {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		elements = append(elements, shared.SnapshotElement{
			Ref:         fmt.Sprintf("@e%d", i+1),
			Selector:    getMapString(m, "selector"),
			Role:        getMapString(m, "role"),
			Name:        getMapString(m, "name"),
			Tag:         getMapString(m, "tag"),
			Type:        getMapString(m, "type"),
			Text:        getMapString(m, "text"),
			Placeholder: getMapString(m, "placeholder"),
			TestID:      getMapString(m, "test_id"),
			Title:       getMapString(m, "title"),
			Alt:         getMapString(m, "alt"),
		})
	}

	total := len(elements)
	if v, ok := raw["total"].(float64); ok && int(v) > 0 {
		total = int(v)
	}

	truncated := false
	if v, ok := raw["truncated"].(bool); ok {
		truncated = v
	}

	return &shared.SnapshotResult{
		SnapshotID: fmt.Sprintf("snap_%d", time.Now().UnixNano()),
		URL:        getMapString(raw, "url"),
		Title:      getMapString(raw, "title"),
		Format:     format,
		Total:      total,
		Truncated:  truncated,
		Elements:   elements,
	}, nil
}

func getMapString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
