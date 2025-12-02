develop_container_dir=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd ) 
echo $develop_container_dir
infra_dir=$(dirname -- "$develop_container_dir") 
echo $infra_dir
root_dir=$(dirname -- "$infra_dir") 
echo $root_dir
DOCKER_FILE_PATH=$develop_container_dir/Dockerfile-base
PREFIX=beclab




docker  build    \
    --build-arg https_proxy=http://192.168.31.140:7890 \
    --build-arg http_proxy=http://192.168.31.140:7890 \
    --build-arg all_proxy=socks5://192.168.31.140:7890 \
    -f ${DOCKER_FILE_PATH} \
    -t ${PREFIX}/fs_lib_develop $root_dir


