// --------------------------------------------------------------------------------------------------------------------
// <copyright file="Program.cs" company="Stack Overflow">
//   Copyright (c) Stack Overflow. All rights reserved.
// </copyright>
// --------------------------------------------------------------------------------------------------------------------

using Microsoft.Extensions.DependencyInjection;
using OpenTelemetry.Metrics;
using OpenTelemetry.Trace;

var services = new ServiceCollection().AddDefaultOpenTelemetry();
await using var provider = services.BuildServiceProvider();
var tracerProvider = provider.GetRequiredService<TracerProvider>();
var meterProvider = provider.GetRequiredService<MeterProvider>();

var target = args.Length > 0 ? args[0] : "world";
using (Telemetry.Source.StartActivity("greet"))
{
    Console.WriteLine(GreetingBuilder.BuildGreeting(target));
}

tracerProvider.ForceFlush();
meterProvider.ForceFlush();
