// <copyright file="Program.cs" company="{{.AuthorName}}">
// Copyright (c) {{.AuthorName}}. All rights reserved.
// </copyright>

var builder = Program.CreateBuilder(args);
var app = builder.Build();
Program.ConfigureApp(app);

app.Run();

/// <summary>
/// Provides shared application construction hooks for runtime and tests.
/// </summary>
public partial class Program
{
    /// <summary>
    /// Creates the application builder for the current entry point.
    /// </summary>
    /// <param name="args">The command-line arguments for the application.</param>
    /// <returns>The configured web application builder.</returns>
    public static WebApplicationBuilder CreateBuilder(string[] args)
    {
        var builder = WebApplication.CreateBuilder(args);

        builder.Services.AddControllers();
        builder.Services.AddEndpointsApiExplorer();
        builder.Services.AddSwaggerGen();
        builder.Services.AddDefaultOpenTelemetry();

        return builder;
    }

    /// <summary>
    /// Applies the shared HTTP middleware and endpoint configuration.
    /// </summary>
    /// <param name="app">The application to configure.</param>
    public static void ConfigureApp(WebApplication app)
    {
        if (app.Environment.IsDevelopment())
        {
            app.UseSwagger();
            app.UseSwaggerUI();
        }

        app.UseAuthorization();
        app.MapControllers();
    }
}
