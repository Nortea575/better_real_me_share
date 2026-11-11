# syntax=docker/dockerfile:1

FROM alpine:3.21 AS data-directory
RUN mkdir -p /data && chown 65532:65532 /data

FROM scratch
ARG TARGETARCH
COPY --chown=65532:65532 linux-${TARGETARCH}/brm-share /brm-share
COPY --from=data-directory --chown=65532:65532 /data /data
USER 65532:65532
VOLUME ["/data"]
EXPOSE 18766
ENTRYPOINT ["/brm-share"]
CMD ["serve"]
