FROM arigaio/atlas-agent:latest as atlas-agent

FROM alpine:3.20.3

RUN apk update && apk add --no-cache curl

COPY --from=atlas-agent /atlas-agent /atlas-agent

COPY entrypoint.sh /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]