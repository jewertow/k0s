FROM alpine:3.12 AS build

ARG VERSION

RUN apk add build-base git go

RUN git clone -b v$VERSION --depth=1 https://github.com/etcd-io/etcd.git
WORKDIR /etcd
RUN CGO_ENABLED=0 go build -o etcd

FROM scratch
COPY --from=build /etcd/etcd /bin/etcd
CMD ["/bin/etcd"]