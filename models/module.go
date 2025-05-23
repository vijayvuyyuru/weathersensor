package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pkg/errors"

	"go.viam.com/rdk/components/sensor"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	"go.viam.com/utils/rpc"
)

const (
	currentWeatherURL = "https://api.weatherapi.com/v1/current.json"
	astronomyURL      = "https://api.weatherapi.com/v1/astronomy.json"
)

// api ref: https://app.swaggerhub.com/apis-docs/WeatherAPI.com/WeatherAPI/1.0.2#/APIs/realtime-weather

var (
	Weathersensor    = resource.NewModel("vijayvuyyuru", "weathersensor", "weathersensor")
	errUnimplemented = errors.New("unimplemented")
)

func init() {
	resource.RegisterComponent(sensor.API, Weathersensor,
		resource.Registration[sensor.Sensor, *Config]{
			Constructor: newWeathersensorWeathersensor,
		},
	)
}

type Config struct {
	TemperatureSensor string `json:"temp-sensor"`
	Zipcode           int    `json:"zipcode"`
	APIKey            string `json:"apikey"`

	/*
		Put config attributes here. There should be public/exported fields
		with a `json` parameter at the end of each attribute.

		Example config struct:
			type Config struct {
				Pin   string `json:"pin"`
				Board string `json:"board"`
				MinDeg *float64 `json:"min_angle_deg,omitempty"`
			}

		If your model does not need a config, replace *Config in the init
		function with resource.NoNativeConfig
	*/

	/* Uncomment this if your model does not need to be validated
	   and has no implicit dependecies. */
	// resource.TriviallyValidateConfig
}

// Validate ensures all parts of the config are valid and important fields exist.
// Returns implicit dependencies based on the config.
// The path is the JSON path in your robot's config (not the `Config` struct) to the
// resource being validated; e.g. "components.0".
func (cfg *Config) Validate(path string) ([]string, []string, error) {
	if cfg.TemperatureSensor == "" {
		return nil, nil, fmt.Errorf(`expected "temp-sensor" attribute for weather module`)
	}
	if cfg.APIKey == "" {
		return nil, nil, fmt.Errorf(`expected "apikey" attribute for weather module`)
	}
	if cfg.Zipcode == 0 {
		return nil, nil, fmt.Errorf(`expected "zipcode" attribute for weather module`)
	}
	return []string{cfg.TemperatureSensor}, nil, nil
}

type weathersensorWeathersensor struct {
	name resource.Name

	logger logging.Logger
	cfg    *Config

	cancelCtx  context.Context
	cancelFunc func()

	temperatureSensor sensor.Sensor
	apiKey            string
	zipcode           int

	// Uncomment this if the model does not have any goroutines that
	// need to be shut down while closing.
	resource.TriviallyCloseable
}

func newWeathersensorWeathersensor(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (sensor.Sensor, error) {
	conf, err := resource.NativeConfig[*Config](rawConf)
	if err != nil {
		return nil, err
	}

	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	s := &weathersensorWeathersensor{
		name:       rawConf.ResourceName(),
		logger:     logger,
		cfg:        conf,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
	}
	if err := s.Reconfigure(ctx, deps, rawConf); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *weathersensorWeathersensor) Name() resource.Name {
	return s.name
}

func (s *weathersensorWeathersensor) Reconfigure(ctx context.Context, deps resource.Dependencies, conf resource.Config) error {
	sensorConfig, err := resource.NativeConfig[*Config](conf)
	if err != nil {
		return err
	}
	s.temperatureSensor, err = sensor.FromDependencies(deps, sensorConfig.TemperatureSensor)
	if err != nil {
		return errors.Wrapf(err, "unable to get temperature sensor %v for weather sensor", sensorConfig.TemperatureSensor)
	}
	s.apiKey = sensorConfig.APIKey
	s.zipcode = sensorConfig.Zipcode
	return nil
}

func (s *weathersensorWeathersensor) NewClientFromConn(ctx context.Context, conn rpc.ClientConn, remoteName string, name resource.Name, logger logging.Logger) (sensor.Sensor, error) {
	panic("not implemented")
}

func (s *weathersensorWeathersensor) Readings(ctx context.Context, extra map[string]interface{}) (map[string]interface{}, error) {
	output := map[string]any{}
	response, err := s.getCurrentWeather()
	if err != nil {
		return nil, err
	}
	astronomyResponse, err := s.getCurrentAstronomy()
	if err != nil {
		return nil, err
	}
	currentWeather := response["current"].(map[string]any)
	output["outside_f"] = currentWeather["temp_f"]
	output["condition"] = currentWeather["condition"].(map[string]any)["text"]
	output["code"] = currentWeather["condition"].(map[string]any)["code"]
	output["cloud_cover_pct"] = currentWeather["cloud"].(float64)
	output["precipitation_inches"] = currentWeather["precip_in"]

	astronomy := astronomyResponse["astronomy"].(map[string]any)["astro"].(map[string]any)
	output["is_day"] = astronomy["is_sun_up"]

	readings, err := s.temperatureSensor.Readings(ctx, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "error getting reading from temp sensor")
	}
	insideTempC := readings["degrees_celsius"].(float64)
	output["inside_f"] = insideTempC*9/5 + 32
	return output, nil
}

func (s *weathersensorWeathersensor) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	panic("not implemented")
}

func (s *weathersensorWeathersensor) Close(context.Context) error {
	// Put close code here
	s.cancelFunc()
	return nil
}

func (s *weathersensorWeathersensor) getCurrentAstronomy() (map[string]any, error) {
	url := fmt.Sprintf("%s?q=%d&dt=%s&key=%s", astronomyURL, s.zipcode, time.Now().Format("2006-01-02"), s.apiKey)
	return s.getWeatherBodyOrCode(url)
}

func (s *weathersensorWeathersensor) getCurrentWeather() (map[string]any, error) {
	url := fmt.Sprintf("%s?q=%d&key=%s", currentWeatherURL, s.zipcode, s.apiKey)
	return s.getWeatherBodyOrCode(url)
}

func (s *weathersensorWeathersensor) getWeatherBodyOrCode(url string) (map[string]any, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		s.logger.Errorf("Error creating request: %v\n", err)
		return nil, err
	}
	req.Header.Add("Accept", "application/json")

	// Send the request
	resp, err := client.Do(req)
	if err != nil {
		s.logger.Errorf("Error making request: %v\n", err)
		return nil, err
	}
	defer resp.Body.Close()

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		s.logger.Errorf("Error reading response body: %v\n", err)
		return nil, err
	}
	var responseJSON map[string]interface{}
	if err := json.Unmarshal(body, &responseJSON); err != nil {
		s.logger.Errorf("Error unmarshaling to map: %v\n", err)
		return nil, err
	}

	// Check status code
	if resp.StatusCode != http.StatusOK {
		s.logger.Errorf("Unexpected status code: %d\n", resp.StatusCode)
		code, ok := responseJSON["code"]
		if !ok {
			return nil, errors.Errorf("request failed with code %d, and no code in response body", resp.StatusCode)
		}
		message, messageOK := responseJSON["message"]
		if !messageOK {
			return nil, errors.Errorf("request failed with code %d, and no message in response body", resp.StatusCode)
		}
		return responseJSON, errors.Errorf("error fetching weather info, code: %d, message: %s", code, message)
	}
	return responseJSON, nil
}

func (s *weathersensorWeathersensor) getCurrentWeatherURL() string {
	return fmt.Sprintf("%s?q=%d&key=%s", currentWeatherURL, s.zipcode, s.apiKey)
}
