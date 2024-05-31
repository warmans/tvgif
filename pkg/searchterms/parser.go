package searchterms

import (
	"fmt"
	"github.com/pkg/errors"
	"github.com/warmans/tvgif/pkg/filter"
	"strconv"
	"strings"
)

type Term struct {
	Field string
	Value Value
	Op    filter.CompOp
}

func MustParse(s string) []Term {
	f, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return f
}

func Parse(s string) ([]Term, error) {
	if s == "" {
		return nil, nil
	}
	return newParser(newScanner(s)).Parse()
}

func newParser(s *scanner) *parser {
	return &parser{s: s}
}

type parser struct {
	s      *scanner
	peeked *token
}

func (p *parser) Parse() ([]Term, error) {
	terms, err := p.parseOuter()
	if err != nil {
		return nil, err
	}
	if _, err := p.requireNext(tagEOF); err != nil {
		return nil, err
	}
	return terms, nil
}

func (p *parser) parseOuter() ([]Term, error) {
	terms := []Term{}
	innerTerms, err := p.parseInner()
	if err != nil {
		return nil, err
	}
	for innerTerms != nil {
		for _, term := range innerTerms {
			terms = append(terms, *term)
		}
		innerTerms, err = p.parseInner()
		if err != nil {
			return nil, err
		}
	}
	return terms, nil
}

func (p *parser) parseInner() ([]*Term, error) {
	tok, err := p.getNext()
	if err != nil {
		return nil, err
	}
	switch tok.tag {
	case tagEOF:
		return nil, nil
	case tagQuotedString:
		return []*Term{{
			Field: "content",
			Value: String(strings.Trim(tok.lexeme, `"`)),
			Op:    filter.CompOpEq,
		}}, nil
	case tagWord:
		words := []string{tok.lexeme}
		next, err := p.peekNext()
		if err != nil {
			return nil, err
		}
		for next.tag == tagWord {
			next, err = p.getNext()
			if err != nil {
				return nil, err
			}
			if word := strings.TrimSpace(next.lexeme); word != "" {
				words = append(words, word)
			}
			next, err = p.peekNext()
			if err != nil {
				return nil, err
			}
		}
		return []*Term{{
			Field: "content",
			Value: String(strings.Join(words, " ")),
			Op:    filter.CompOpFuzzyLike,
		}}, nil
	case tagMention:
		mentionText, err := p.requireNext(tagQuotedString, tagWord, tagEOF)
		if err != nil {
			return nil, err
		}
		return []*Term{{
			Field: "actor",
			Value: String(strings.ToLower(mentionText.lexeme)),
			Op:    filter.CompOpEq,
		}}, nil
	case tagPublication:
		mentionText, err := p.requireNext(tagQuotedString, tagWord, tagEOF)
		if err != nil {
			return nil, err
		}
		return []*Term{{
			Field: "publication",
			Value: String(strings.ToLower(mentionText.lexeme)),
			Op:    filter.CompOpEq,
		}}, nil
	case tagId:
		mentionText, err := p.requireNext(tagQuotedString, tagWord, tagEOF)
		if err != nil {
			return nil, err
		}
		return p.expandIDCondition(strings.ToLower(mentionText.lexeme))
	default:
		return nil, errors.Errorf("unexpected token '%s'", tok)
	}
}

// peekNext gets the next token without advancing.
func (p *parser) peekNext() (token, error) {
	if p.peeked != nil {
		return *p.peeked, nil
	}
	t, err := p.getNext()
	if err != nil {
		return token{}, err
	}
	p.peeked = &t
	return t, nil
}

// getNext advances to the next token
func (p *parser) getNext() (token, error) {
	if p.peeked != nil {
		t := *p.peeked
		p.peeked = nil
		return t, nil
	}
	t, err := p.s.next()
	if err != nil {
		return token{}, err
	}
	return t, err
}

// requireNext advances to the next token and asserts it is one of the given tags.
func (p *parser) requireNext(oneOf ...tag) (token, error) {
	t, err := p.getNext()
	if err != nil {
		return t, err
	}
	for _, tag := range oneOf {
		if t.tag == tag {
			return t, nil
		}
	}
	return token{}, errors.Errorf("expected one of '%v', found '%s'", oneOf, t.tag)
}

func (p *parser) expandIDCondition(lexme string) ([]*Term, error) {
	if strings.HasPrefix(lexme, "s") {
		parts := strings.Split(lexme, "e")
		if len(parts) == 0 || len(parts) > 2 {
			return nil, fmt.Errorf("id had an unexpected format: %s", lexme)
		}
		series, err := strconv.Atoi(strings.TrimLeft(parts[0], "s0"))
		if err != nil {
			return nil, fmt.Errorf("could not parse series '%s' from given id %s", parts[0], lexme)
		}
		if len(parts) == 1 {
			return []*Term{{
				Field: "series",
				Value: Int(int64(series)),
				Op:    filter.CompOpEq,
			}}, nil
		}
		if len(parts) == 2 {
			episode, err := strconv.Atoi(strings.TrimLeft(parts[1], "e0"))
			if err != nil {
				return nil, fmt.Errorf("could not parse episode '%s' from given id %s", parts[1], lexme)
			}
			return []*Term{{
				Field: "series",
				Value: Int(int64(series)),
				Op:    filter.CompOpEq,
			}, {
				Field: "episode",
				Value: Int(int64(episode)),
				Op:    filter.CompOpEq,
			}}, nil
		}
		return nil, fmt.Errorf("id had an unexpected format: %s", lexme)
	}
	if strings.HasPrefix(lexme, "e") {
		episode, err := strconv.Atoi(strings.TrimLeft(lexme, "e0"))
		if err != nil {
			return nil, fmt.Errorf("could not parse episode from given id %s", lexme)
		}
		return []*Term{{
			Field: "episode",
			Value: Int(int64(episode)),
			Op:    filter.CompOpEq,
		}}, nil
	}
	return nil, fmt.Errorf("id had an unexpected format: %s", lexme)
}
