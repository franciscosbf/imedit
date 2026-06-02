# imedit

A distributed system for image manipulation. Supports transformations such as cropping, resizing, rotating, formatting and filtering (sepia and gray scale filters).

## Overview

The system is composed of two services: **manager** and **processor**. Due to their stateless nature, both can be deployed with multiple replicas. The **manager** serves as the entry point for user registration, login, and various operations related to image storage and transformations. Each user can upload, download and transform images. Transformation requests are propagated through an event broker - **RabbitMQ** - and then received by **processor** services responsible for executing those operations.

WebSocket connections are used for notifying users upon transformations state, i.e., if they succeeded as expected or something went wrong.

Regarding caching and storage, [Redis](https://redis.io/) tracks recently accessed images and their associated metadata (when requested). [MinIO](https://www.min.io/) blob storage (similar to [Amazon S3](https://aws.amazon.com/s3/)) holds images along with the metadata and [MySQL](https://www.mysql.com/) manages registered users and maintains a list of uploaded images per user,to improve performance when users request multiple images in a single operation.

![Architecture Overview](assets/architecture-overview.png)

## Endpoints

The **manager** service exposes two APIs: **user** and **image**. All endpoints adopt the same error response format, with the exception of errors originating from the WebSocket connection itself (e.g. `Sec-WebSocket-Accept` header value is invalid) in the endpoint `/v1/image/ws` which return a plain text message:

```json
{
  "reason": "<reason>",
  "message": "<message>",
}
```

In terms of authentication method, [JWT](https://jwt.io/) is enforced by all endpoints except `/v1/user/register` and `/v1/user/login`, requiring the header `Authorization: Bearer <token>` to be present in the request.

On successful request processing, every endpoint returns the OK status code (200).

### User API

##### Common Request Header

- Content-Type: application/json

#### POST /v1/user/register

##### Request Body

```json
{
  "username": "<username>",
  "password": "<password>",
}
```

#### POST /v1/user/login

##### Request Body

```json
{
  "username": "<username>",
  "password": "<password>",
}
```

##### Response Header

- Content-Type: application/json

##### Response Body

```json
{
  "token": "<token>",
}
```

#### PUT /v1/user/password

##### Request Body

```json
{
  "username": "<username>",
  "current_password": "<current password>",
  "new_password": "<new password>",
}
```

### Image API

The system supports only two image formats: **png** and **jpeg**.

#### POST /v1/image/upload

The endpoint requires the image to be uploaded using the [multipart/form-data](https://developer.mozilla.org/en-US/docs/Web/HTTP/Guides/MIME_types#multipartform-data) MIME format. The image is included as an individual file part within the multipart payload.

##### Request Header

- Content-Type: multipart/form-data; boundary=\<boundary\>

##### Request Body

```text
--<boundary>
Content-Disposition: form-data; name="<name>"; filename="<file name>"
Content-Type: image/<format>

<image content>
```

#### GET /v1/image/single/{image_id}

Image is downloaded using the [multipart/form-data](https://developer.mozilla.org/en-US/docs/Web/HTTP/Guides/MIME_types#multipartform-data) MIME format, where the image is included as a single part of the multipart request.

##### Request Path Variable

- image_id: image id

##### Request Header

- Accept: multipart/form-data

##### Response Header

- Content-Type: multipart/form-data; boundary=\<boundary\>

##### Response Body

```text
--<boundary>
Content-Disposition: form-data; name="<name>"; filename="<file name>"
Content-Type: image/<format>

<image content>
```

#### GET /v1/image/paginated

Images are downloaded using the [multipart/form-data](https://developer.mozilla.org/en-US/docs/Web/HTTP/Guides/MIME_types#multipartform-data) MIME format, where each image is included as a single part of the multipart request.

##### Request Query Parameters

- page: page number
- limit: number of images per page

##### Request Header

- Accept: multipart/form-data

##### Response Header

- Content-Type: multipart/form-data; boundary=\<boundary\>

##### Response Body

```text
--<boundary>
Content-Disposition: form-data; name="<name>"; filename="<file name>"
Content-Type: image/<format>

<image content>

--<boundary>
Content-Disposition: form-data; name="<name>"; filename="<file name>"
Content-Type: image/<format>

<image content>
...
```

#### GET /v1/image/meta/{image_id}

##### Request Path Variable

- image_id: image id

##### Response Header

- Content-Type: application/json

##### Response Body

**last_modified** date is specified in UTC format. **encoding** can be either **png** or **jpeg**, as these are the only supported image formats.

```json
{
  "image_id": "<image id>",
  "name": "<name>",
  "encoding": "<encoding>",
  "size": 6291456,
  "width": 1920,
  "height": 1080,
  "last_modified": "<last modified>",
}
```

#### PUT /v1/image/transform

##### Request Header

- Content-Type: application/json

##### Request Body

At least one of the five available transformations (**crop**, **resize**, **rotation** and **format**) must be specified. **format** can be either **png** or **jpeg**. Operations are performed in the order listed below.

```json
{
  "image_id": "<image id>",
  "transformations": {
    "crop": {
      "width": 1920,
      "height": 1080,
      "x": 420,
      "y": 0,
    },
    "resize": {
      "width": 1280,
      "height": 720,
    },
    "filter": {
      "grayscale": true,
      "sepia": false,
    },
    "rotate": 90,
    "format": "<format>",
  }
}
```

##### Response Header

- Content-Type: application/json

##### Response Body

```json
{
  "transformation_id": "<transformation id>",
}
```

#### GET /v1/image/ws

Transformation events are sent through WebSocket connection.

##### Events

Events are represented as JSON objects. The `type` field identifies the event type, while the `event` field contains the event payload.

###### Successful Transformation

```json
{
  "type": "transformation_successful",
  "event": {
    "image_id": "<image id>",
    "transformation_id": "<transformation id>",
  },
}
```

###### Failed Transformation

```json
{
  "type": "transformation_failure",
  "event": {
    "image_id": "<image id>",
    "transformation_id": "<transformation id>",
    "reason": "<reason>",
  },
}
```

###### Unexpected Error

```json
{
  "type": "unexpected_error",
  "event": {
    "reason": "<reason>",
  },
}
```
