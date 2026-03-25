use std::collections::HashMap;

#[derive(Clone, PartialEq, ::prost::Message)]
pub struct Message {
    #[prost(string, tag = "1")]
    pub originator: String,
    #[prost(string, tag = "2")]
    pub topic: String,
    #[prost(string, tag = "3")]
    pub action: String,
    #[prost(bytes = "vec", tag = "4")]
    pub payload: Vec<u8>,
    #[prost(string, tag = "5")]
    pub message_id: String,
    #[prost(int64, tag = "6")]
    pub timestamp_ms: i64,
    #[prost(map = "string, string", tag = "7")]
    pub metadata: HashMap<String, String>,
}
