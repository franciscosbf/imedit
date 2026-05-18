#!/usr/bin/env sh

docker run --detach --name rabbit \
  --env RABBITMQ_DEFAULT_USER=user \
  --env RABBITMQ_DEFAULT_PASS=password \
  --publish 15672:15672 \
  --publish 5672:5672 \
  -v $(PWD)/enabled_plugins:/etc/rabbitmq/enabled_plugins \
  rabbitmq:4.2.5-management
