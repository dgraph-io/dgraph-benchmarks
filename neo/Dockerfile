# Dockerfile for DGraph

FROM golang:1.24.1
MAINTAINER Manish Jain <manishrjain@gmail.com>

RUN apt-get update && apt-get install -y --no-install-recommends \
	git \
	libbz2-dev \
	libgflags-dev \
	libsnappy-dev \
	lsof \
	openjdk-7-jre \
	zlib1g-dev \
	&& rm -rf /var/lib/apt/lists/*

# Install and set up RocksDB.
RUN mkdir /installs && cd /installs && \
	git clone --branch v4.1 https://github.com/facebook/rocksdb.git
RUN cd /installs/rocksdb && make shared_lib && make install
ENV LD_LIBRARY_PATH "/usr/local/lib"

RUN cd /installs && \
	wget https://github.com/github/git-lfs/releases/download/v1.1.0/git-lfs-linux-amd64-1.1.0.tar.gz && \
	tar -xzvf git-lfs-linux-amd64-1.1.0.tar.gz && \
	cd /installs/git-lfs-1.1.0 && ./install.sh

RUN echo v0.1
RUN mkdir -p /go/src/github.com/hypermodeinc && \
  cd /go/src/github.com/hypermodeinc && git clone https://github.com/hypermodeinc/dgraph-benchmarks
RUN cd /go/src/github.com/hypermodeinc/dgraph-benchmarks/neo && go get -v .

COPY neo4j.tar.gz /installs/neo4j.tar.gz
RUN cd /installs && tar -xzvf neo4j.tar.gz
COPY neo4j-server.properties /installs/neo4j-community-2.3.1/conf/neo4j-server.properties
EXPOSE 7474

# docker tag -f aaaaaaaaa dgraph/neo4j:latest
# docker push dgraph/neo4j
# docker run -p 7474:7474 -i -t -v /mnt/neo4j:/data dgraph/neo4j
