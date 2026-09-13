package browser

// markdownExtractionScript contains the JavaScript code for extracting page content as Markdown
const markdownExtractionScript = `
(function(options) {
    'use strict';

    const config = {
        strategy: options.strategy || 'auto',
        includeMetadata: options.includeMetadata !== false,
        includeLinks: options.includeLinks !== false,
        includeTables: options.includeTables !== false,
        maxLength: options.maxLength || 0
    };

    const result = {
        markdown: '',
        metadata: null,
        warnings: []
    };

    // Utility functions
    function escapeMarkdown(text) {
        if (!text) return '';
        return text
            .replace(/\\/g, '\\\\')
            .replace(/\*/g, '\\*')
            .replace(/_/g, '\\_')
            .replace(/\[/g, '\\[')
            .replace(/\]/g, '\\]')
            .replace(/\(/g, '\\(')
            .replace(/\)/g, '\\)')
            .replace(/#/g, '\\#')
            .replace(/\+/g, '\\+')
            .replace(/-/g, '\\-')
            .replace(/\./g, '\\.')
            .replace(/!/g, '\\!')
            .replace(/\|/g, '\\|');
    }

    function cleanText(text) {
        if (!text) return '';
        return text
            .replace(/\s+/g, ' ')
            .trim();
    }

    function getHeadingLevel(tag) {
        const match = tag.match(/^H(\d)$/i);
        return match ? parseInt(match[1]) : 0;
    }

    // Extract metadata from the page
    function extractMetadata() {
        const metadata = {
            title: document.title || '',
            url: window.location.href,
            language: document.documentElement.lang || '',
            extraction_strategy: config.strategy,
            timestamp: new Date().toISOString()
        };

        // Try to get better title from meta tags
        const ogTitle = document.querySelector('meta[property="og:title"]');
        if (ogTitle && ogTitle.content) {
            metadata.title = ogTitle.content;
        }

        return metadata;
    }

    // Find the main article content
    function findArticleContent() {
        // Priority order for article detection
        const selectors = [
            'article',
            '[role="main"]',
            'main',
            '.post-content',
            '.article-content',
            '.entry-content',
            '.content',
            '#content',
            '.post',
            '.article'
        ];

        for (const selector of selectors) {
            const element = document.querySelector(selector);
            if (element && element.textContent.trim().length > 100) {
                return element;
            }
        }

        // Fallback: find the element with the most text content
        const candidates = document.querySelectorAll('div, section');
        let bestCandidate = null;
        let maxTextLength = 0;

        candidates.forEach(el => {
            const text = el.textContent.trim();
            if (text.length > maxTextLength && text.length > 200) {
                // Check it's not a navigation or footer
                const tag = el.tagName.toLowerCase();
                const classes = el.className.toLowerCase();
                const id = (el.id || '').toLowerCase();

                if (!classes.includes('nav') &&
                    !classes.includes('menu') &&
                    !classes.includes('footer') &&
                    !classes.includes('sidebar') &&
                    !id.includes('nav') &&
                    !id.includes('menu') &&
                    !id.includes('footer') &&
                    !id.includes('sidebar')) {
                    maxTextLength = text.length;
                    bestCandidate = el;
                }
            }
        });

        return bestCandidate || document.body;
    }

    // Convert element to Markdown
    function elementToMarkdown(element, depth = 0) {
        if (!element) return '';

        const tagName = element.tagName ? element.tagName.toUpperCase() : '';
        let md = '';

        // Skip hidden elements and scripts/styles
        if (element.nodeType === Node.ELEMENT_NODE) {
            const style = window.getComputedStyle(element);
            if (style.display === 'none' || style.visibility === 'hidden') {
                return '';
            }

            if (['SCRIPT', 'STYLE', 'NOSCRIPT', 'IFRAME', 'SVG', 'NAV', 'FOOTER', 'HEADER'].includes(tagName)) {
                return '';
            }
        }

        // Handle text nodes
        if (element.nodeType === Node.TEXT_NODE) {
            const text = element.textContent;
            if (text.trim()) {
                return cleanText(text);
            }
            return '';
        }

        // Handle different element types
        switch (tagName) {
            case 'H1':
            case 'H2':
            case 'H3':
            case 'H4':
            case 'H5':
            case 'H6':
                const level = getHeadingLevel(tagName);
                const headingText = cleanText(element.textContent);
                if (headingText) {
                    md = '\n\n' + '#'.repeat(level) + ' ' + headingText + '\n\n';
                }
                break;

            case 'P':
                const pText = processChildren(element, depth);
                if (pText.trim()) {
                    md = '\n\n' + pText + '\n\n';
                }
                break;

            case 'BR':
                md = '\n';
                break;

            case 'HR':
                md = '\n\n---\n\n';
                break;

            case 'STRONG':
            case 'B':
                const boldText = processChildren(element, depth);
                if (boldText.trim()) {
                    md = '**' + boldText.trim() + '**';
                }
                break;

            case 'EM':
            case 'I':
                const italicText = processChildren(element, depth);
                if (italicText.trim()) {
                    md = '*' + italicText.trim() + '*';
                }
                break;

            case 'CODE':
                const codeText = element.textContent;
                if (codeText.trim()) {
                    md = '` + "`" + `' + codeText + '` + "`" + `';
                }
                break;

            case 'PRE':
                const preText = element.textContent;
                if (preText.trim()) {
                    // Try to detect language from class
                    let lang = '';
                    const codeEl = element.querySelector('code');
                    if (codeEl && codeEl.className) {
                        const langMatch = codeEl.className.match(/language-(\w+)/);
                        if (langMatch) {
                            lang = langMatch[1];
                        }
                    }
                    md = '\n\n` + "```" + `' + lang + '\n' + preText.trim() + '\n` + "```" + `\n\n';
                }
                break;

            case 'BLOCKQUOTE':
                const quoteText = processChildren(element, depth);
                if (quoteText.trim()) {
                    const lines = quoteText.trim().split('\n');
                    md = '\n\n' + lines.map(l => '> ' + l).join('\n') + '\n\n';
                }
                break;

            case 'A':
                if (config.includeLinks) {
                    const linkText = cleanText(element.textContent);
                    const href = element.getAttribute('href');
                    if (linkText && href && !href.startsWith('javascript:')) {
                        // Make relative URLs absolute
                        let absoluteHref = href;
                        try {
                            absoluteHref = new URL(href, window.location.href).href;
                        } catch (e) {}
                        md = '[' + linkText + '](' + absoluteHref + ')';
                    } else if (linkText) {
                        md = linkText;
                    }
                } else {
                    md = cleanText(element.textContent);
                }
                break;

            case 'IMG':
                const alt = element.getAttribute('alt') || 'image';
                const src = element.getAttribute('src');
                if (src) {
                    let absoluteSrc = src;
                    try {
                        absoluteSrc = new URL(src, window.location.href).href;
                    } catch (e) {}
                    md = '![' + cleanText(alt) + '](' + absoluteSrc + ')';
                }
                break;

            case 'UL':
                const ulItems = Array.from(element.children)
                    .filter(li => li.tagName === 'LI')
                    .map(li => '- ' + processChildren(li, depth + 1).trim())
                    .filter(item => item.length > 2);
                if (ulItems.length > 0) {
                    md = '\n\n' + ulItems.join('\n') + '\n\n';
                }
                break;

            case 'OL':
                const olItems = Array.from(element.children)
                    .filter(li => li.tagName === 'LI')
                    .map((li, i) => (i + 1) + '. ' + processChildren(li, depth + 1).trim())
                    .filter(item => item.length > 3);
                if (olItems.length > 0) {
                    md = '\n\n' + olItems.join('\n') + '\n\n';
                }
                break;

            case 'TABLE':
                if (config.includeTables) {
                    md = tableToMarkdown(element);
                }
                break;

            case 'DIV':
            case 'SECTION':
            case 'ARTICLE':
            case 'MAIN':
            case 'SPAN':
            default:
                md = processChildren(element, depth);
                break;
        }

        return md;
    }

    function processChildren(element, depth) {
        let result = '';
        element.childNodes.forEach(child => {
            result += elementToMarkdown(child, depth);
        });
        return result;
    }

    function tableToMarkdown(table) {
        const rows = Array.from(table.querySelectorAll('tr'));
        if (rows.length === 0) return '';

        const headers = [];
        const dataRows = [];

        rows.forEach((row, index) => {
            const cells = Array.from(row.querySelectorAll('th, td'));
            const cellTexts = cells.map(cell => cleanText(cell.textContent) || ' ');

            if (index === 0 && row.querySelector('th')) {
                headers.push(...cellTexts);
            } else if (headers.length === 0 && index === 0) {
                // First row becomes header if no th elements
                headers.push(...cellTexts);
            } else {
                dataRows.push(cellTexts);
            }
        });

        if (headers.length === 0) return '';

        let md = '\n\n| ' + headers.join(' | ') + ' |\n';
        md += '| ' + headers.map(() => '---').join(' | ') + ' |\n';

        dataRows.forEach(row => {
            // Ensure row has same number of columns as headers
            while (row.length < headers.length) {
                row.push(' ');
            }
            md += '| ' + row.slice(0, headers.length).join(' | ') + ' |\n';
        });

        return md + '\n';
    }

    // DOM-based extraction (full page structure)
    function extractDom() {
        return elementToMarkdown(document.body);
    }

    // Article-focused extraction
    function extractArticle() {
        const article = findArticleContent();
        return elementToMarkdown(article);
    }

    // Accessibility tree extraction
    function extractAccessibility() {
        const walker = document.createTreeWalker(
            document.body,
            NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT,
            {
                acceptNode: function(node) {
                    if (node.nodeType === Node.TEXT_NODE) {
                        return node.textContent.trim() ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT;
                    }
                    const tagName = node.tagName.toUpperCase();
                    if (['SCRIPT', 'STYLE', 'NOSCRIPT', 'SVG', 'NAV'].includes(tagName)) {
                        return NodeFilter.FILTER_REJECT;
                    }
                    const style = window.getComputedStyle(node);
                    if (style.display === 'none' || style.visibility === 'hidden') {
                        return NodeFilter.FILTER_REJECT;
                    }
                    return NodeFilter.FILTER_ACCEPT;
                }
            }
        );

        let md = '';
        let node;
        while (node = walker.nextNode()) {
            if (node.nodeType === Node.TEXT_NODE) {
                const text = cleanText(node.textContent);
                if (text) {
                    md += text + ' ';
                }
            } else if (node.nodeType === Node.ELEMENT_NODE) {
                const tagName = node.tagName.toUpperCase();
                if (['H1', 'H2', 'H3', 'H4', 'H5', 'H6'].includes(tagName)) {
                    const level = getHeadingLevel(tagName);
                    md += '\n\n' + '#'.repeat(level) + ' ';
                } else if (tagName === 'P' || tagName === 'DIV') {
                    md += '\n\n';
                } else if (tagName === 'BR') {
                    md += '\n';
                } else if (tagName === 'LI') {
                    md += '\n- ';
                }
            }
        }

        return md;
    }

    // Auto strategy - choose best approach based on page structure
    function extractAuto() {
        // Check if page has article-like structure
        const hasArticle = document.querySelector('article, [role="main"], main, .post-content, .article-content');

        if (hasArticle) {
            result.warnings.push('Using article extraction strategy');
            config.strategy = 'article';
            return extractArticle();
        }

        // Default to DOM extraction
        result.warnings.push('Using DOM extraction strategy');
        config.strategy = 'dom';
        return extractDom();
    }

    // Main extraction function
    function extract() {
        try {
            // Get metadata first
            if (config.includeMetadata) {
                result.metadata = extractMetadata();
            }

            // Extract based on strategy
            let markdown;
            switch (config.strategy) {
                case 'article':
                    markdown = extractArticle();
                    break;
                case 'dom':
                    markdown = extractDom();
                    break;
                case 'accessibility':
                    markdown = extractAccessibility();
                    break;
                case 'auto':
                default:
                    markdown = extractAuto();
                    break;
            }

            // Clean up the markdown
            markdown = markdown
                .replace(/\n{3,}/g, '\n\n')  // Remove excessive newlines
                .replace(/[ \t]+/g, ' ')      // Normalize spaces
                .trim();

            // Update metadata with final strategy
            if (result.metadata) {
                result.metadata.extraction_strategy = config.strategy;
            }

            // Apply max length if specified
            if (config.maxLength > 0 && markdown.length > config.maxLength) {
                markdown = markdown.substring(0, config.maxLength);
                result.warnings.push('Content truncated to ' + config.maxLength + ' characters');
            }

            result.markdown = markdown;

        } catch (err) {
            result.warnings.push('Extraction error: ' + err.message);
            const fallbackText =
                (document.body && (document.body.innerText || document.body.textContent)) ||
                (document.documentElement && (document.documentElement.innerText || document.documentElement.textContent)) ||
                '';
            result.markdown = cleanText(fallbackText);
        }

        return result;
    }

    return extract();
})
`
