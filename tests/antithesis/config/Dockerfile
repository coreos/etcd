FROM alpine AS build
ARG IMAGE_REPOSITORY=
ARG IMAGE_TAG=latest
COPY manifests/ /manifests/
RUN sed -i "s/etcd-client:latest/etcd-client:${IMAGE_TAG}/g" /manifests/client.yaml && \
    sed -i "s/etcd-server:latest/etcd-server:${IMAGE_TAG}/g" /manifests/default-etcd-3-replicas.yaml && \
    sed -i "s|image: etcd-client|image: ${IMAGE_REPOSITORY}etcd-client|g" /manifests/client.yaml && \
    sed -i "s|image: etcd-server|image: ${IMAGE_REPOSITORY}etcd-server|g" /manifests/default-etcd-3-replicas.yaml

FROM scratch
COPY --from=build /manifests/ /manifests/
