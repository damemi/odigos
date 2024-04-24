FROM openjdk:11 AS java-builder
RUN apt update -y && apt install -y g++ systemtap-sdt-dev make maven gradle