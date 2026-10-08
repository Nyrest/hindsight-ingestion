package connectors

import (
	"bytes"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"golang.org/x/net/html"
)

type imageReference struct {
	start, end   int
	url, caption string
}

type markdownCodeSpan struct {
	start, end int
	kind       ast.NodeKind
}

type imageSpanParser struct {
	parser.InlineParser
	ends map[ast.Node]int
}

func (p *imageSpanParser) Parse(parent ast.Node, reader text.Reader, context parser.Context) ast.Node {
	n := p.InlineParser.Parse(parent, reader, context)
	if _, ok := n.(*ast.Image); ok {
		_, position := reader.Position()
		p.ends[n] = position.Start
	}
	return n
}

var notionContainers = regexp.MustCompile(`(?i)</?(?:callout|columns|column|details|summary|synced_block|synced_block_reference|table|tr|td|colgroup|col)(?:\s[^>]*|)?>`)

func markdownImages(source []byte) []imageReference {
	parseSource, offsets := inlineParseSource(source)
	capture := &imageSpanParser{InlineParser: parser.NewLinkParser(), ends: map[ast.Node]int{}}
	inlines := parser.DefaultInlineParsers()
	for i := range inlines {
		if inlines[i].Value == parser.NewLinkParser() {
			inlines[i].Value = capture
		}
	}
	p := parser.NewParser(parser.WithBlockParsers(parser.DefaultBlockParsers()...), parser.WithInlineParsers(inlines...), parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...))
	root := p.Parse(text.NewReader(parseSource))
	var refs []imageReference
	codeDepth := 0
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Image:
			if codeDepth > 0 {
				return ast.WalkSkipChildren, nil
			}
			refs = append(refs, imageReference{n.Pos(), capture.ends[n], html.UnescapeString(string(util.UnescapePunctuations(n.Destination))), string(n.Text(parseSource))})
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML:
			if n.Segments.Len() > 0 {
				start, end := n.Segments.At(0).Start, n.Segments.At(n.Segments.Len()-1).Stop
				images, depth := htmlImages(parseSource[start:end], start, codeDepth)
				refs, codeDepth = append(refs, images...), depth
			}
		case *ast.HTMLBlock:
			start, end := htmlBlockRange(n)
			if end > start {
				images, depth := htmlImages(parseSource[start:end], start, codeDepth)
				refs, codeDepth = append(refs, images...), depth
			}
		}
		return ast.WalkContinue, nil
	})
	sort.Slice(refs, func(i, j int) bool { return refs[i].start < refs[j].start })
	for i := range refs {
		if offsets != nil {
			refs[i].start, refs[i].end = offsets[refs[i].start], offsets[refs[i].end]
		}
	}
	return refs
}

func htmlBlockRange(n *ast.HTMLBlock) (start, end int) {
	if n.HasClosure() {
		start, end = n.ClosureLine.Start, n.ClosureLine.Stop
	}
	if n.Lines().Len() > 0 {
		start = n.Lines().At(0).Start
		end = max(end, n.Lines().At(n.Lines().Len()-1).Stop)
	}
	return start, end
}

// Keep byte offsets back to the original Markdown while removing Notion's container indentation.
func inlineParseSource(source []byte) ([]byte, []int) {
	spans := containerSpans(source)
	if len(spans) == 0 {
		return source, nil
	}
	masked := maskContainers(source, spans)
	var parsed []byte
	var offsets []int
	depth, start := 0, 0
	for _, line := range bytes.SplitAfter(masked, []byte("\n")) {
		trim := min(depth, len(line)-len(bytes.TrimLeft(line, "\t")))
		for i := trim; i < len(line); i++ {
			parsed = append(parsed, line[i])
			offsets = append(offsets, start+i)
		}
		end := start + len(line)
		tags := sort.Search(len(spans), func(i int) bool { return spans[i][0] >= end })
		depth = containerDepth(source, spans[:tags], depth)
		spans = spans[tags:]
		start = end
	}
	return parsed, append(offsets, len(source))
}

func containerSpans(source []byte) [][]int {
	spans := notionContainers.FindAllIndex(source, -1)
	if len(spans) == 0 {
		return spans
	}
	codeSpans := markdownCodeSpans(source)
	filtered := spans[:0]
	depth := 0
	for _, span := range spans {
		protected := slices.ContainsFunc(codeSpans, func(code markdownCodeSpan) bool {
			if span[0] < code.start || span[0] >= code.end {
				return false
			}
			// Notion's structural tabs also look like indented code before normalization.
			return code.kind != ast.KindCodeBlock || depth == 0 || !isContainerIndent(source, span[0], depth)
		})
		if protected {
			continue
		}
		filtered = append(filtered, span)
		depth = containerDepth(source, [][]int{span}, depth)
	}
	return filtered
}

func isContainerIndent(source []byte, position, depth int) bool {
	lineStart := bytes.LastIndexByte(source[:position], '\n') + 1
	indent := source[lineStart:position]
	tabs := len(indent) - len(bytes.TrimLeft(indent, "\t"))
	return len(indent) == tabs && tabs <= depth
}

func markdownCodeSpans(source []byte) []markdownCodeSpan {
	// Code examples must not change container indentation.
	var codeSpans []markdownCodeSpan
	root := parser.NewParser(parser.WithBlockParsers(parser.DefaultBlockParsers()...), parser.WithInlineParsers(parser.DefaultInlineParsers()...)).Parse(text.NewReader(source))
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			if n.Lines().Len() > 0 {
				codeSpans = append(codeSpans, markdownCodeSpan{n.Lines().At(0).Start, n.Lines().At(n.Lines().Len() - 1).Stop, n.Kind()})
			}
		case *ast.CodeSpan:
			for child := n.FirstChild(); child != nil; child = child.NextSibling() {
				segment := child.(*ast.Text).Segment
				codeSpans = append(codeSpans, markdownCodeSpan{segment.Start, segment.Stop, n.Kind()})
			}
		}
		return ast.WalkContinue, nil
	})
	return codeSpans
}

func maskContainers(source []byte, spans [][]int) []byte {
	masked := bytes.Clone(source)
	for _, span := range spans {
		for i := span[0]; i < span[1]; i++ {
			if masked[i] != '\n' && masked[i] != '\r' {
				masked[i] = ' '
			}
		}
	}
	return masked
}

func containerDepth(source []byte, spans [][]int, depth int) int {
	for _, span := range spans {
		tag := source[span[0]:span[1]]
		if bytes.HasPrefix(tag, []byte("</")) {
			depth = max(0, depth-1)
		} else if !bytes.HasSuffix(tag, []byte("/>")) {
			depth++
		}
	}
	return depth
}

func htmlImages(source []byte, offset, codeDepth int) ([]imageReference, int) {
	z := html.NewTokenizer(bytes.NewReader(source))
	var refs []imageReference
	position := offset
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			return refs, codeDepth
		}
		start := position
		position += len(z.Raw())
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken && kind != html.EndTagToken {
			continue
		}
		token := z.Token()
		codeDepth = htmlCodeDepth(token.Data, kind, codeDepth)
		if codeDepth > 0 || token.Data != "img" || kind == html.EndTagToken {
			continue
		}
		ref := htmlImageReference(token, start, position)
		if strings.TrimSpace(ref.url) != "" {
			refs = append(refs, ref)
		}
	}
}

func htmlCodeDepth(tag string, kind html.TokenType, depth int) int {
	switch tag {
	case "pre", "code", "script", "style":
		if kind == html.EndTagToken {
			return max(0, depth-1)
		}
		if kind == html.StartTagToken {
			return depth + 1
		}
	}
	return depth
}

func htmlImageReference(token html.Token, start, end int) imageReference {
	ref := imageReference{start: start, end: end}
	for _, attr := range token.Attr {
		switch attr.Key {
		case "src":
			ref.url = attr.Val
		case "alt":
			ref.caption = attr.Val
		}
	}
	return ref
}
