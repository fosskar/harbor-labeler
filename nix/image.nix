{
  dockerTools,
  harbor-labeler,
  cacert,
}:

dockerTools.streamLayeredImage {
  name = "harbor-labeler";
  tag = harbor-labeler.version;

  contents = [
    harbor-labeler
    # TLS roots for the Harbor API; custom CAs are mounted by the chart.
    cacert
  ];

  config = {
    Entrypoint = [ "/bin/harbor-labeler" ];
    User = "3000:3000";
  };
}
