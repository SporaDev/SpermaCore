cmake_minimum_required(VERSION 3.20)
project(SpermaCorePlugins C CXX)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

set(SPERMACORE_MODULES_DIR "${CMAKE_SOURCE_DIR}/modules")
set(SPERMACORE_INCLUDE_DIR "${CMAKE_SOURCE_DIR}/include")

file(GLOB module_dirs LIST_DIRECTORIES true "${SPERMACORE_MODULES_DIR}/*")
foreach(module_dir IN LISTS module_dirs)
    if(NOT IS_DIRECTORY "${module_dir}" OR NOT EXISTS "${module_dir}/src")
        continue()
    endif()

    get_filename_component(module_name "${module_dir}" NAME)
    file(GLOB module_sources CONFIGURE_DEPENDS
        "${module_dir}/src/*.c"
        "${module_dir}/src/*.cpp")
    if(NOT module_sources)
        continue()
    endif()

    add_library("${module_name}" SHARED ${module_sources})
    target_include_directories("${module_name}" PRIVATE "${SPERMACORE_INCLUDE_DIR}")

    # sc-16: build modules with -Werror. c++20 designated initializers
    # allow omitting trailing fields, so for c++ we only disable
    # -Wmissing-field-initializers.
    if(MSVC)
        target_compile_options("${module_name}" PRIVATE /W4 /WX)
    else()
        target_compile_options("${module_name}" PRIVATE
            -Wall -Wextra -Werror
            $<$<COMPILE_LANGUAGE:CXX>:-Wno-missing-field-initializers -Wno-format-truncation>)
    endif()

    set_target_properties("${module_name}" PROPERTIES
        PREFIX ""
        LIBRARY_OUTPUT_DIRECTORY "${module_dir}/build"
        RUNTIME_OUTPUT_DIRECTORY "${module_dir}/build"
        ARCHIVE_OUTPUT_DIRECTORY "${module_dir}/build"
    )

    if(WIN32)
        target_link_libraries("${module_name}" PRIVATE ws2_32)
        if(module_name STREQUAL "websearch")
            target_link_libraries("${module_name}" PRIVATE winhttp)
        endif()
    else()
        # dlopen/dlsym for dynamic loading of tdlib and other runtime libraries.
        target_link_libraries("${module_name}" PRIVATE ${CMAKE_DL_LIBS})
    endif()
endforeach()
