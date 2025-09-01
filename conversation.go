package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

type Message struct {
	Role      string    `json:"role"` // "user" or "assistant"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	Hash      string    `json:"hash"`
	PrevHash  string    `json:"prevHash"`
}

type MessageWithHash struct {
	Message
}

func (m *MessageWithHash) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.Message)
}

func (m *MessageWithHash) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &m.Message)
}

func calculateMessageHash(prevHash, role, content string, timestamp time.Time) string {
	messageData := struct {
		PrevHash  string    `json:"prevHash"`
		Role      string    `json:"role"`
		Content   string    `json:"content"`
		Timestamp time.Time `json:"timestamp"`
	}{
		PrevHash:  prevHash,
		Role:      role,
		Content:   content,
		Timestamp: timestamp,
	}

	jsonData, err := json.Marshal(messageData)
	if err != nil {
		// Fallback to simple string concatenation if marshaling fails
		data := prevHash + role + content + timestamp.Format(time.RFC3339Nano)
		hash := sha256.Sum256([]byte(data))
		return hex.EncodeToString(hash[:])
	}

	hash := sha256.Sum256(jsonData)
	return hex.EncodeToString(hash[:])
}

type Conversation struct {
	Name     string    `json:"name"`
	Messages []Message `json:"messages"`
	Parent   *string   `json:"parent,omitempty"`
}

func loadConversation(name string) (*Conversation, error) {
	filename := fmt.Sprintf(".%s.figaro.json", name)

	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			// Create new conversation if file doesn't exist
			return &Conversation{
				Name:     name,
				Messages: []Message{},
			}, nil
		}
		return nil, fmt.Errorf("failed to read conversation file: %w", err)
	}

	var conv Conversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("failed to parse conversation file: %w", err)
	}

	logEvent("info", "Loaded conversation", "name", name, "message_count", len(conv.Messages))
	return &conv, nil
}

func validateForkExists(forkName string) error {
	filename := fmt.Sprintf(".%s.figaro.json", forkName)
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return fmt.Errorf("fork conversation '%s' does not exist", forkName)
	}
	return nil
}

func (c *Conversation) save() error {
	filename := fmt.Sprintf(".%s.figaro.json", c.Name)

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal conversation: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("failed to write conversation file: %w", err)
	}

	logEvent("info", "Saved conversation", "name", c.Name, "message_count", len(c.Messages))
	return nil
}

func (c *Conversation) addMessage(role, content string) {
	timestamp := time.Now()
	prevHash := ""
	if len(c.Messages) > 0 {
		prevHash = c.Messages[len(c.Messages)-1].Hash
	}

	hash := calculateMessageHash(prevHash, role, content, timestamp)

	c.Messages = append(c.Messages, Message{
		Role:      role,
		Content:   content,
		Timestamp: timestamp,
		Hash:      hash,
		PrevHash:  prevHash,
	})
}

func (c *Conversation) addUserMessage(content string) {
	c.addMessage("user", content)
}

func (c *Conversation) addAssistantMessage(content string) {
	c.addMessage("assistant", content)
}

func (c *Conversation) toAnthropicMessages() ([]anthropic.MessageParam, error) {
	var messages []anthropic.MessageParam

	if c.Parent != nil {
		parentConvo, err := loadConversation(*c.Parent)
		if err != nil {
			return nil, fmt.Errorf("Parent conversation with name %q does not exist: %w", *c.Parent, err)
		}
		parentMessages, err := parentConvo.toAnthropicMessages()
		if err != nil {
			return nil, err
		}
		for _, msg := range parentMessages {
			messages = append(messages, msg)
		}
	}

	for _, msg := range c.Messages {
		switch msg.Role {
		case "user":
			messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content)))
		case "assistant":
			messages = append(messages, anthropic.NewAssistantMessage(anthropic.NewTextBlock(msg.Content)))
		}
	}

	return messages, nil
}

