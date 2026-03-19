package v1

import (
	"context"
	"fmt"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
)

type ImageValidator interface {
	Validate() error
}

func (iu *ImageUpload) Validate() error {
	if iu.Image.Name == "" {
		return fmt.Errorf("image name must be provided in header Content-Disposition of sub-part")
	}

	if iu.Image.Type == "" {
		return fmt.Errorf("image type must be provided in header Content-Type of sub-part")
	}

	return nil
}

func (i *Image) Validate() error {
	if i.Id == "" {
		return fmt.Errorf("image id cannot be empty")
	}

	return nil
}

func (p *Pagination) Validate() error {
	if p.Page == 0 {
		return fmt.Errorf("page must be greater than zero")
	}

	if p.Limit == 0 {
		return fmt.Errorf("limit must be greater than zero")
	}

	return nil
}

func (it *ImageTransformations) Validate() error {
	if it.Id == "" {
		return fmt.Errorf("image id cannot be empty")
	}

	if transformations := &it.Transformations; transformations.Resize == nil &&
		transformations.Crop == nil &&
		transformations.Rotate == nil &&
		transformations.Format == nil {
		return fmt.Errorf("you must provide at least one transformation")
	}

	return nil
}

func ImageValidate() middleware.Middleware {
	return func(h middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			if v, ok := req.(ImageValidator); ok {
				if err := v.Validate(); err != nil {
					return nil, errors.BadRequest("VALIDATOR", err.Error()).WithCause(err)
				}
			}

			return h(ctx, req)
		}
	}
}
