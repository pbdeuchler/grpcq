use std::{
    collections::HashSet,
    path::{Path, PathBuf},
};

use heck::ToSnakeCase;
use proc_macro2::{Span, TokenStream};
use quote::{format_ident, quote};
use syn::{parse_str, LitStr, Type};

pub type Result<T> = std::result::Result<T, std::io::Error>;

pub fn compile_protos<P, I>(protos: &[P], includes: &[I]) -> Result<()>
where
    P: AsRef<Path>,
    I: AsRef<Path>,
{
    Config::new().compile_protos(protos, includes)
}

pub fn compile_proto(proto: impl AsRef<Path>) -> Result<()> {
    let proto = proto.as_ref().to_path_buf();
    let include = proto
        .parent()
        .unwrap_or_else(|| Path::new("."))
        .to_path_buf();

    compile_protos(&[proto], &[include])
}

pub struct Config {
    inner: prost_build::Config,
}

impl Config {
    pub fn new() -> Self {
        Self {
            inner: prost_build::Config::new(),
        }
    }

    pub fn out_dir(&mut self, out_dir: impl Into<PathBuf>) -> &mut Self {
        self.inner.out_dir(out_dir);
        self
    }

    pub fn include_file(&mut self, path: impl Into<PathBuf>) -> &mut Self {
        self.inner.include_file(path);
        self
    }

    pub fn compile_protos<P, I>(&mut self, protos: &[P], includes: &[I]) -> Result<()>
    where
        P: AsRef<Path>,
        I: AsRef<Path>,
    {
        self.inner
            .service_generator(Box::new(GrpcqServiceGenerator::default()));
        self.inner.compile_protos(protos, includes)
    }
}

impl Default for Config {
    fn default() -> Self {
        Self::new()
    }
}

#[derive(Default)]
struct GrpcqServiceGenerator;

impl prost_build::ServiceGenerator for GrpcqServiceGenerator {
    fn generate(&mut self, service: prost_build::Service, buf: &mut String) {
        buf.push_str(&generate_service_modules(&service).to_string());
        buf.push('\n');
    }
}

fn generate_service_modules(service: &prost_build::Service) -> TokenStream {
    if let Some(error) = validate_service(service) {
        return compile_error(&error);
    }

    let consumer = generate_consumer_module(service);
    let producer = generate_producer_module(service);

    quote! {
        #consumer
        #producer
    }
}

fn generate_consumer_module(service: &prost_build::Service) -> TokenStream {
    #[cfg(feature = "tonic")]
    {
        return generate_tonic_consumer_module(service);
    }

    #[cfg(not(feature = "tonic"))]
    {
        return generate_plain_consumer_module(service);
    }
}

#[cfg(not(feature = "tonic"))]
fn generate_plain_consumer_module(service: &prost_build::Service) -> TokenStream {
    let service_trait = format_ident!("{}", service.name);
    let consumer_struct = format_ident!("{}Consumer", service.name);
    let module_name = format_ident!("{}_consumer", service.name.to_snake_case());
    let service_name = LitStr::new(&fully_qualified_service_name(service), Span::call_site());

    let trait_methods = service.methods.iter().map(|method| {
        let method_name = format_ident!("{}", method.name);
        let input_type = parse_type(&method.input_type);
        let output_type = parse_type(&method.output_type);

        quote! {
            async fn #method_name(&self, req: #input_type) -> grpcq::Result<#output_type>;
        }
    });

    let register_methods = service.methods.iter().map(|method| {
        let method_name = format_ident!("{}", method.name);
        let method_proto_name = LitStr::new(&method.proto_name, Span::call_site());
        let input_type = parse_type(&method.input_type);

        quote! {
            {
                let inner = std::sync::Arc::clone(&self.inner);
                registry.register(#service_name, #method_proto_name, move |message| {
                    let inner = std::sync::Arc::clone(&inner);
                    async move {
                        let request = <#input_type as ::prost::Message>::decode(message.payload.as_slice())
                            .map_err(|source| grpcq::Error::RequestDecode {
                                service: #service_name.to_string(),
                                method: #method_proto_name.to_string(),
                                source,
                            })?;
                        let _ = inner.#method_name(request).await?;
                        Ok(())
                    }
                });
            }
        }
    });

    quote! {
        pub mod #module_name {
            use super::*;

            #[grpcq::async_trait]
            pub trait #service_trait: Send + Sync + 'static {
                #(#trait_methods)*
            }

            pub struct #consumer_struct<T: #service_trait> {
                inner: std::sync::Arc<T>,
            }

            impl<T: #service_trait> #consumer_struct<T> {
                pub fn new(inner: T) -> Self {
                    Self {
                        inner: std::sync::Arc::new(inner),
                    }
                }

                pub fn from_arc(inner: std::sync::Arc<T>) -> Self {
                    Self { inner }
                }
            }

            impl<T: #service_trait> grpcq::ServiceRegistrar for #consumer_struct<T> {
                fn register(&self, registry: &grpcq::Registry) {
                    #(#register_methods)*
                }

                fn service_name(&self) -> &'static str {
                    #service_name
                }
            }
        }
    }
}

#[cfg(feature = "tonic")]
fn generate_tonic_consumer_module(service: &prost_build::Service) -> TokenStream {
    let service_trait = format_ident!("{}", service.name);
    let consumer_struct = format_ident!("{}Consumer", service.name);
    let module_name = format_ident!("{}_consumer", service.name.to_snake_case());
    let service_name = LitStr::new(&fully_qualified_service_name(service), Span::call_site());

    let trait_methods = service.methods.iter().map(|method| {
        let method_name = format_ident!("{}", method.name);
        let input_type = parse_type(&method.input_type);
        let output_type = parse_type(&method.output_type);

        quote! {
            async fn #method_name(
                &self,
                req: grpcq::tonic::Request<#input_type>,
            ) -> std::result::Result<grpcq::tonic::Response<#output_type>, grpcq::tonic::Status>;
        }
    });

    let register_methods = service.methods.iter().map(|method| {
        let method_name = format_ident!("{}", method.name);
        let method_proto_name = LitStr::new(&method.proto_name, Span::call_site());
        let input_type = parse_type(&method.input_type);

        quote! {
            {
                let inner = std::sync::Arc::clone(&self.inner);
                registry.register(#service_name, #method_proto_name, move |message| {
                    let inner = std::sync::Arc::clone(&inner);
                    async move {
                        let request = <#input_type as ::prost::Message>::decode(message.payload.as_slice())
                            .map_err(|source| grpcq::Error::RequestDecode {
                                service: #service_name.to_string(),
                                method: #method_proto_name.to_string(),
                                source,
                            })?;
                        let _ = inner
                            .#method_name(grpcq::tonic::Request::new(request))
                            .await
                            .map_err(|status| grpcq::Error::other(status.to_string()))?
                            .into_inner();
                        Ok(())
                    }
                });
            }
        }
    });

    quote! {
        pub mod #module_name {
            use super::*;

            #[grpcq::tonic::async_trait]
            pub trait #service_trait: Send + Sync + 'static {
                #(#trait_methods)*
            }

            pub struct #consumer_struct<T: #service_trait> {
                inner: std::sync::Arc<T>,
            }

            impl<T: #service_trait> #consumer_struct<T> {
                pub fn new(inner: T) -> Self {
                    Self {
                        inner: std::sync::Arc::new(inner),
                    }
                }

                pub fn from_arc(inner: std::sync::Arc<T>) -> Self {
                    Self { inner }
                }
            }

            impl<T: #service_trait> grpcq::ServiceRegistrar for #consumer_struct<T> {
                fn register(&self, registry: &grpcq::Registry) {
                    #(#register_methods)*
                }

                fn service_name(&self) -> &'static str {
                    #service_name
                }
            }
        }
    }
}

fn generate_producer_module(service: &prost_build::Service) -> TokenStream {
    let module_name = format_ident!("{}_producer", service.name.to_snake_case());
    let producer_struct = format_ident!("{}Producer", service.name);
    let service_name = LitStr::new(&fully_qualified_service_name(service), Span::call_site());

    let default_methods = service.methods.iter().map(|method| {
        let method_name = format_ident!("{}", method.name);
        let input_type = parse_type(&method.input_type);
        let with_options_method = format_ident!("{}_with_options", method.name);

        quote! {
            pub async fn #method_name(&self, req: #input_type) -> grpcq::Result<()> {
                self.#with_options_method(req, grpcq::CallOptions::default())
                    .await
            }
        }
    });

    let with_options_methods = service.methods.iter().map(|method| {
        let method_name = format_ident!("{}_with_options", method.name);
        let input_type = parse_type(&method.input_type);
        let method_proto_name = LitStr::new(&method.proto_name, Span::call_site());

        quote! {
            pub async fn #method_name(
                &self,
                req: #input_type,
                options: grpcq::CallOptions,
            ) -> grpcq::Result<()> {
                self.client
                    .invoke(#service_name, #method_proto_name, &req, options)
                    .await
            }
        }
    });

    quote! {
        pub mod #module_name {
            use super::*;

            pub struct #producer_struct {
                client: grpcq::Client,
            }

            impl #producer_struct {
                pub fn new(adapter: grpcq::SharedAdapter, config: grpcq::ClientConfig) -> Self {
                    Self {
                        client: grpcq::Client::new(adapter, config),
                    }
                }

                #(#default_methods)*
                #(#with_options_methods)*
            }
        }
    }
}

fn validate_service(service: &prost_build::Service) -> Option<String> {
    let streaming_methods = service
        .methods
        .iter()
        .filter(|method| method.client_streaming || method.server_streaming)
        .map(|method| method.proto_name.clone())
        .collect::<Vec<_>>();
    if !streaming_methods.is_empty() {
        return Some(format!(
            "grpcq-build only supports unary RPC methods; {} contains streaming RPCs: {}",
            fully_qualified_service_name(service),
            streaming_methods.join(", ")
        ));
    }

    let mut seen = HashSet::new();
    let duplicate = service
        .methods
        .iter()
        .find(|method| !seen.insert(method.name.clone()));
    duplicate.map(|method| {
        format!(
            "grpcq-build generated duplicate Rust method name `{}` for service {}",
            method.name,
            fully_qualified_service_name(service)
        )
    })
}

fn fully_qualified_service_name(service: &prost_build::Service) -> String {
    if service.package.is_empty() {
        service.proto_name.clone()
    } else {
        format!("{}.{}", service.package, service.proto_name)
    }
}

fn parse_type(path: &str) -> Type {
    parse_str(path).expect("prost-build should emit parsable Rust type paths")
}

fn compile_error(message: &str) -> TokenStream {
    let message = LitStr::new(message, Span::call_site());
    quote! {
        compile_error!(#message);
    }
}
