package vehicle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/andig/evcc/api"
	"github.com/andig/evcc/provider"
	"github.com/andig/evcc/util"
	"github.com/andig/evcc/util/oauth"
	"github.com/andig/evcc/util/request"
	"github.com/andig/evcc/util/sponsor"
	"github.com/andig/evcc/vehicle/tronity"
	"golang.org/x/oauth2"
)

// Tronity is an api.Vehicle implementation for the Tronity api
type Tronity struct {
	*embed
	*request.Helper
	log   *util.Logger
	oc    *oauth2.Config
	vid   string
	bulkG func() (interface{}, error)
}

func init() {
	registry.Add("tronity", NewTronityFromConfig)
}

// NewTronityFromConfig creates a new Tronity vehicle
func NewTronityFromConfig(other map[string]interface{}) (api.Vehicle, error) {
	cc := struct {
		embed       `mapstructure:",squash"`
		Credentials ClientCredentials
		Tokens      Tokens
		VIN         string
		Cache       time.Duration
	}{
		Cache: interval,
	}

	if err := util.DecodeOther(other, &cc); err != nil {
		return nil, err
	}

	if err := cc.Credentials.Error(); err != nil {
		return nil, err
	}

	if !sponsor.IsAuthorized() {
		return nil, errors.New("tronity requires evcc sponsorship, register at https://cloud.evcc.io")
	}

	// authenticated http client with logging injected to the tronity client
	log := util.NewLogger("tronity")

	oc, err := tronity.OAuth2Config(cc.Credentials.ID, cc.Credentials.Secret)
	if err != nil {
		return nil, err
	}

	v := &Tronity{
		log:    log,
		embed:  &cc.embed,
		Helper: request.NewHelper(log),
		oc:     oc,
	}

	var ts oauth2.TokenSource

	// https://app.platform.tronity.io/docs#tag/Authentication
	if err := cc.Tokens.Error(); err != nil {
		// use app flow if we don't have tokens
		ts = oauth.RefreshTokenSource(&oauth2.Token{}, v)
	} else {
		// use provided tokens generated by code flow
		ctx := context.WithValue(context.Background(), oauth2.HTTPClient, request.NewHelper(log).Client)
		ts = oc.TokenSource(ctx, &oauth2.Token{
			AccessToken:  cc.Tokens.Access,
			RefreshToken: cc.Tokens.Refresh,
			Expiry:       time.Now(),
		})
	}

	// replace client transport with authenticated transport
	v.Client.Transport = &oauth2.Transport{
		Source: ts,
		Base:   v.Client.Transport,
	}

	vehicles, err := v.vehicles()
	if err != nil {
		return nil, err
	}

	if cc.VIN == "" && len(vehicles) == 1 {
		v.vid = vehicles[0].ID
	} else {
		for _, vehicle := range vehicles {
			if vehicle.VIN == strings.ToUpper(cc.VIN) {
				v.vid = vehicle.ID
			}
		}
	}

	if v.vid == "" {
		return nil, errors.New("vin not found")
	}

	v.bulkG = provider.NewCached(v.bulk, cc.Cache).InterfaceGetter()

	return v, nil
}

// RefreshToken performs token refresh by logging in with app context
func (v *Tronity) RefreshToken(_ *oauth2.Token) (*oauth2.Token, error) {
	data := struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		GrantType    string `json:"grant_type"`
	}{
		ClientID:     v.oc.ClientID,
		ClientSecret: v.oc.ClientSecret,
		GrantType:    "app",
	}

	req, err := request.New(http.MethodPost, v.oc.Endpoint.TokenURL, request.MarshalJSON(data), request.JSONEncoding)
	if err != nil {
		return nil, err
	}

	var token oauth2.Token
	err = request.NewHelper(v.log).DoJSON(req, &token)

	return &token, err
}

// vehicles implements the vehicles api
func (v *Tronity) vehicles() ([]tronity.Vehicle, error) {
	uri := fmt.Sprintf("%s/v1/vehicles", tronity.URI)

	var res tronity.Vehicles
	err := v.GetJSON(uri, &res)

	return res.Data, err
}

// bulk implements the bulk api
func (v *Tronity) bulk() (interface{}, error) {
	uri := fmt.Sprintf("%s/v1/vehicles/%s/bulk", tronity.URI, v.vid)

	var res tronity.Bulk
	err := v.GetJSON(uri, &res)

	return res, err
}

// SoC implements the api.Vehicle interface
func (v *Tronity) SoC() (float64, error) {
	res, err := v.bulkG()

	if res, ok := res.(tronity.Bulk); err == nil && ok {
		return res.Level, nil
	}

	return 0, err
}

var _ api.ChargeState = (*Tronity)(nil)

// Status implements the api.ChargeState interface
func (v *Tronity) Status() (api.ChargeStatus, error) {
	status := api.StatusA // disconnected
	res, err := v.bulkG()

	if res, ok := res.(tronity.Bulk); err == nil && ok {
		if res.Charging == "Charging" {
			status = api.StatusC
		}
	}

	return status, err
}

var _ api.VehicleRange = (*Tronity)(nil)

// Range implements the api.VehicleRange interface
func (v *Tronity) Range() (int64, error) {
	res, err := v.bulkG()

	if res, ok := res.(tronity.Bulk); err == nil && ok {
		return int64(res.Range), nil
	}

	return 0, err
}

var _ api.VehicleStartCharge = (*Tronity)(nil)

func (v *Tronity) post(uri string) error {
	resp, err := v.Post(uri, "", nil)
	if err == nil {
		err = request.ResponseError(resp)
	}

	// ignore HTTP 405
	if err != nil {
		if err2, ok := err.(request.StatusError); ok && err2.HasStatus(http.StatusMethodNotAllowed) {
			err = nil
		}
	}

	return err
}

// StartCharge implements the api.VehicleStartCharge interface
func (v *Tronity) StartCharge() error {
	uri := fmt.Sprintf("%s/v1/vehicles/%s/charge_start", tronity.URI, v.vid)
	return v.post(uri)
}

var _ api.VehicleStopCharge = (*Tronity)(nil)

// StopCharge implements the api.VehicleStopCharge interface
func (v *Tronity) StopCharge() error {
	uri := fmt.Sprintf("%s/v1/vehicles/%s/charge_stop", tronity.URI, v.vid)
	return v.post(uri)
}