package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const supportedRuntimeCipherVersion = int64(1)

type RuntimeSecretRepository interface {
	LoadRuntimeSecretBundle(context.Context, int64, int64, int64) (vnextstore.RuntimeSecretBundle, error)
}

type StoreSecretProvider struct {
	repository RuntimeSecretRepository
	box        *secretbox.Box
}

func NewStoreSecretProvider(repository RuntimeSecretRepository, box *secretbox.Box) (*StoreSecretProvider, error) {
	if repository == nil || box == nil {
		return nil, errors.New("runtime secret repository and secret box are required")
	}
	return &StoreSecretProvider{repository: repository, box: box}, nil
}

func (provider *StoreSecretProvider) Materialize(
	ctx context.Context,
	metadata resolver.EndpointMetadata,
	credentialID routing.CredentialID,
) (SecretMaterial, error) {
	if metadata.SiteID <= 0 || metadata.EndpointID <= 0 || credentialID <= 0 {
		return SecretMaterial{}, errors.New("runtime secret identity is invalid")
	}
	bundle, err := provider.repository.LoadRuntimeSecretBundle(ctx, metadata.SiteID, metadata.EndpointID, int64(credentialID))
	if err != nil {
		return SecretMaterial{}, errors.New("runtime secret material is unavailable")
	}
	if bundle.CredentialCipherVersion != supportedRuntimeCipherVersion {
		return SecretMaterial{}, errors.New("runtime credential cipher version is unsupported")
	}
	credential, err := provider.box.Open(secretbox.PurposeSiteCredential, secretbox.Identity{
		RecordID: int64(credentialID), OwnerID: metadata.SiteID,
	}, bundle.CredentialCipher)
	if err != nil {
		return SecretMaterial{}, errors.New("runtime credential could not be decrypted")
	}
	defer clear(credential)
	if len(bytes.TrimSpace(credential)) == 0 {
		return SecretMaterial{}, errors.New("runtime credential is empty")
	}

	headers, err := decodeEndpointHeaders(metadata.HeaderTemplate)
	if err != nil {
		return SecretMaterial{}, errors.New("endpoint header template is invalid")
	}
	if len(bundle.SecretHeadersCipher) > 0 {
		if bundle.SecretHeadersCipherVersion != supportedRuntimeCipherVersion {
			return SecretMaterial{}, errors.New("runtime secret header cipher version is unsupported")
		}
		plaintext, openErr := provider.box.Open(secretbox.PurposeSiteSecretHeaders, secretbox.Identity{
			RecordID: metadata.EndpointID, OwnerID: metadata.SiteID,
		}, bundle.SecretHeadersCipher)
		if openErr != nil {
			return SecretMaterial{}, errors.New("runtime secret headers could not be decrypted")
		}
		secretHeaders, decodeErr := decodeEndpointHeaders(plaintext)
		clear(plaintext)
		if decodeErr != nil {
			return SecretMaterial{}, errors.New("runtime secret headers are invalid")
		}
		for name, values := range secretHeaders {
			headers[name] = append([]string(nil), values...)
		}
	}
	return SecretMaterial{Credential: string(credential), Headers: headers}, nil
}

func decodeEndpointHeaders(raw []byte) (http.Header, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return make(http.Header), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil || len(object) > 64 {
		return nil, errors.New("endpoint headers must be a bounded JSON object")
	}
	result := make(http.Header, len(object))
	totalBytes := 0
	for rawName, rawValue := range object {
		name := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(rawName))
		if name == "" || forbiddenEndpointHeader(name) {
			return nil, errors.New("endpoint header name is not allowed")
		}
		values, err := decodeHeaderValues(rawValue)
		if err != nil || len(values) == 0 {
			return nil, errors.New("endpoint header value is invalid")
		}
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				return nil, errors.New("endpoint header value is invalid")
			}
			totalBytes += len(name) + len(value)
			if totalBytes > 32<<10 {
				return nil, errors.New("endpoint headers exceed the safety limit")
			}
			result.Add(name, value)
		}
	}
	return result, nil
}

func decodeHeaderValues(raw json.RawMessage) ([]string, error) {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return []string{single}, nil
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) == nil && multiple != nil {
		return multiple, nil
	}
	return nil, errors.New("header value must be a string or string array")
}

var _ SecretProvider = (*StoreSecretProvider)(nil)
