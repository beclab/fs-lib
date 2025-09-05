
export host_code_directory="/var/wangzhong/fs-lib"
docker run    --env https_proxy=http://192.168.31.200:7890 --env http_proxy=http://192.168.31.200:7890  --env no_proxy=localhost,127.0.0.1,192.168.31.   --name temp_fs_lib_develop  -v $host_code_directory:/opt/fs-lib  --net=host -d beclab/fs_lib_develop