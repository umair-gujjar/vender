extern crate bindgen;

use std::env;
use std::path::PathBuf;

fn main() {
    if cfg!(target_os = "linux") {
        println!("cargo:rustc-link-lib=pigpio");
        println!("cargo:rustc-link-lib=pthread");
        println!("cargo:rustc-link-lib=rt");
        println!("cargo:rustc-link-search=static=../pigpio");
    }

    let out_path = PathBuf::from(env::var("OUT_DIR").unwrap());
    bindgen::Builder::default()
        .header("wrapper.h")
        .generate()
        .expect("Unable to generate bindings")
        .write_to_file(out_path.join("bindings.rs"))
        .expect("Couldn't write bindings!");
}
