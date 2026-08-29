// Package mqtt implements a core.Output that publishes samples to an MQTT
// broker using the Eclipse Paho client.
package mqtt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"ecofloc/core"
)

type MQTTOutput struct {
	cfg    Config
	client mqtt.Client
}

// Config holds the MQTT output settings, loaded from mqtt.json.
type Config struct {
	Broker   string `json:"broker"`
	Topic    string `json:"topic"`
	ClientID string `json:"client_id"`
	Username string `json:"username"`
	Password string `json:"password"`
}

var defaultConfig = Config{
	Broker:  	"tcp://localhost:1883",
	Topic:  	"ecofloc",
	ClientID:	"ecofloc",
}

// init function will be called when the mqtt package is imported, before the main function
func init() {
	core.RegisterOutput("mqtt", MQTTCreator)
}

// MQTTCreator creates a new MQTT output instance.
func MQTTCreator(_ core.Config) (core.Output, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}

	//set mqtt client options
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetConnectTimeout(5 * time.Second).
		SetAutoReconnect(true)
	//set username and password if provided
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}

	//create mqtt client
	client := mqtt.NewClient(opts)
	//connect to mqtt broker returns a handle to the connection
	handle := client.Connect()
	// wait for connection with timeout
	if !handle.WaitTimeout(5 * time.Second) {
		return nil, fmt.Errorf("mqtt: timed out connecting to %s", cfg.Broker)
	}
	// check if the connection failed
	if err := handle.Error(); err != nil {
		return nil, fmt.Errorf("mqtt: connecting to %s: %w", cfg.Broker, err)
	}

	//connection established
	return &MQTTOutput{cfg: cfg, client: client}, nil
}

// configPath returns the path of the mqtt.json which should be placed in the same directory as the executable
func configPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("mqtt: locating executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "mqtt.json"), nil
}

// LoadConfig parses mqtt.json
// If the file cannot be read or parsed, a warning is printed and the default
// configuration is returned.
func LoadConfig() (Config, error) {
	c := defaultConfig
	path, err := configPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: mqtt: locating config file:", err, "- using default configuration")
		return defaultConfig, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: mqtt: reading config file:", err, "- using default configuration")
		return defaultConfig, nil
	}
	if err := json.Unmarshal(data, &c); err != nil {
		fmt.Fprintln(os.Stderr, "warning: mqtt: parsing config file:", err, "- using default configuration")
		return defaultConfig, nil
	}
	// Set default values if not specified
	if c.Broker == "" {
		c.Broker = defaultConfig.Broker
	}
	if c.Topic == "" {
		c.Topic = defaultConfig.Topic
	}
	if c.ClientID == "" {
		c.ClientID = defaultConfig.ClientID
	}
	return c, nil
}

// returns the name of the output.
func (o *MQTTOutput) Name() string { return "mqtt" }

// Write publishes the batch of samples as a JSON payload to the configured topic.
func (o *MQTTOutput) Write(samples []core.Sample) error {
	payload, err := json.Marshal(samples)
	if err != nil {
		return err
	}
	//publish the payload to the topic
	handle := o.client.Publish(o.cfg.Topic, 0, false, payload)
	//wait for the publish to complete with a timeout
	if !handle.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("mqtt: publish to %q timed out", o.cfg.Topic)
	}
	//check if the publish failed
	return handle.Error()
}

// Close disconnects from the broker.
func (o *MQTTOutput) Close() error {
	if o.client != nil && o.client.IsConnected() {
		// disconnect from the broker with a timeout for pending operations
		o.client.Disconnect(200)
	}
	return nil
}
